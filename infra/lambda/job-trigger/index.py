"""team1-lambda-job-trigger.

SQS(team1-sqs-job-trigger)를 소비해 실제 K8s Job을 만든다. orderapi(sa-ingest-api)는
sqs:SendMessage 권한만 갖고 Job 생성 RBAC은 갖지 않는다 — 클러스터 변경 권한을
이 Lambda 하나로 좁히는 게 이 SQS+Lambda 경로를 쓰는 이유다(infra/job-trigger.tf,
infra/irsa.tf 주석 참고). RBAC 자체는 infra/k8s/job-trigger-rbac.yaml에서
ai-trader/replay 네임스페이스에 한해 create/get/list/watch/delete만 부여한다.

메시지 바디는 orderapi/jobtrigger.Request와 같은 모양(JSON):
  {"jobType": "ai-trader"|"replay", "date": "YYYY-MM-DD",
   "speed": 60.0, "orderBucket": "...", "shardCount": 4,
   "fromTs": 0, "toTs": 0}

**아직 실제 클러스터로 손 검증하지 못했다** — 이 세션에서 이 IAM 사용자로는
EKS 클러스터 RBAC 접근 권한이 없어서(CLAUDE.md의 "Kafka/Redis authentication"
섹션 하단 참고), boto3/botocore 소스와 aws-iam-authenticator가 쓰는 것과 동일한
공개된 토큰 생성 알고리즘을 근거로 작성했다 — trader/bot.RealBedrockClient·
orderapi/matching/recorder의 AWS_MSK_IAM 코드와 같은 "실제 SDK 소스는 확인했지만
실제 대상으로 검증은 못 함" 상태다.
"""

import base64
import json
import logging
import os
import ssl
import tempfile
import urllib.error
import urllib.request
import uuid

import boto3
import botocore.session
from botocore.signers import RequestSigner

logger = logging.getLogger()
logger.setLevel(logging.INFO)

# ECR/네임스페이스/서비스어카운트는 전부 이미 실제 AWS에 존재하는 고정값이다
# (docs/deployment-env-vars.md "컨테이너 이미지" 섹션, infra/k8s/*/serviceaccount.yaml
# 참고) — Terraform 환경변수로 새로 안 뽑고 여기 상수로 둔 이유는, 이 Lambda 리소스
# 자체(job-trigger.tf)를 건드리지 않고 애플리케이션 코드만으로 이 기능을 완성하기
# 위해서다(계정/리전은 이 프로젝트 전체가 공유하는 고정값이라 바뀔 일이 없음).
ECR_REPO = "727646470302.dkr.ecr.ap-northeast-2.amazonaws.com/team1-truss"
AI_TRADER_NAMESPACE = "ai-trader"
AI_TRADER_SERVICE_ACCOUNT = "sa-ai-trader"
REPLAY_NAMESPACE = "replay"
REPLAY_SERVICE_ACCOUNT = "sa-replay-engine"

STS_TOKEN_EXPIRES_SECONDS = 60
K8S_REQUEST_TIMEOUT_SECONDS = 25

_eks_client = boto3.client("eks")
_sts_client = boto3.client("sts")

# Lambda 실행 환경(microVM)은 같은 컨테이너로 여러 호출을 재사용하므로, 클러스터
# 엔드포인트/CA는 콜드 스타트당 한 번만 조회해 모듈 전역에 캐싱한다 — 매 호출마다
# eks:DescribeCluster를 부르는 건 불필요한 지연/API 콜이다.
_cluster_cache = {}


def handler(event, context):
    errors = []
    for record in event.get("Records", []):
        try:
            _process_record(record)
        except Exception:
            logger.exception("job-trigger 레코드 처리 실패 (messageId=%s)", record.get("messageId"))
            errors.append(record.get("messageId"))
    if errors:
        # 예외를 던져야 SQS가 이 배치를 실패로 보고 재시도(→결국 DLQ)한다. Job
        # 생성은 메시지 ID 기반 결정적 이름을 쓰므로(아래 _job_name) 재시도돼도
        # 이미 성공한 Job을 다시 만들려다 409(이미 있음)로 끝나 안전하다.
        raise RuntimeError(f"{len(errors)}건 처리 실패: {errors}")
    return {"status": "ok", "processed": len(event.get("Records", []))}


def _process_record(record):
    body = json.loads(record.get("body", "{}"))
    job_type = body.get("jobType")
    message_id = record.get("messageId", uuid.uuid4().hex)

    if job_type == "ai-trader":
        manifest = _build_ai_trader_job(body, message_id)
        namespace = AI_TRADER_NAMESPACE
    elif job_type == "replay":
        manifest = _build_replay_job(body, message_id)
        namespace = REPLAY_NAMESPACE
    else:
        # orderapi가 이미 jobType을 검증하지만(jobtrigger.ValidateRequest),
        # 재검증 없이 신뢰하지 않는다 — 이 큐에 다른 발신자가 생길 수도 있다.
        raise ValueError(f"알 수 없는 jobType: {job_type!r}")

    logger.info("Job 생성 시도: namespace=%s name=%s", namespace, manifest["metadata"]["name"])
    status, resp_body = _create_job(namespace, manifest)
    if status == 201:
        logger.info("Job 생성 완료: %s", manifest["metadata"]["name"])
    elif status == 409:
        # 같은 메시지가 재시도돼 이미 만들어진 Job과 이름이 같은 경우 — 정상.
        logger.info("Job이 이미 존재함(재시도, 정상): %s", manifest["metadata"]["name"])
    else:
        raise RuntimeError(f"K8s Job 생성 실패 (status={status}): {resp_body}")


def _job_name(prefix, date_str, message_id):
    # 같은 메시지가 재전달돼도 항상 같은 이름이 나오게 messageId를 그대로 쓴다
    # (SQS의 messageId는 같은 메시지의 재수신 사이에 바뀌지 않는다) — 이름
    # 결정성이 위 handler의 "재시도해도 안전" 근거다. K8s 오브젝트 이름 규칙
    # (소문자/숫자/'-', 63자 이하)에 맞춰 date는 하이픈 없이, messageId는 앞 12자만 쓴다.
    short_id = message_id.replace("-", "")[:12]
    return f"{prefix}-{date_str.replace('-', '')}-{short_id}"


def _base_args(body):
    args = [f"-date={body['date']}"]
    if body.get("speed") is not None:
        args.append(f"-speed={body['speed']}")
    if body.get("orderBucket"):
        args.append(f"-order-bucket={body['orderBucket']}")
    return args


def _build_ai_trader_job(body, message_id):
    name = _job_name("ai-trader", body["date"], message_id)
    return {
        "apiVersion": "batch/v1",
        "kind": "Job",
        "metadata": {"name": name, "namespace": AI_TRADER_NAMESPACE},
        "spec": {
            # 세션 가드가 이미 중복 실행을 막으므로(trader의 Claim이 409면 즉시
            # 종료), 재시도해도 똑같이 실패할 뿐이다 — 기본 재시도로 Fargate
            # 콜드스타트만 낭비하지 않게 0으로 둔다(CLAUDE.md "Job cleanup" 참고).
            "backoffLimit": 0,
            "ttlSecondsAfterFinished": 3600,
            "template": {
                "spec": {
                    "serviceAccountName": AI_TRADER_SERVICE_ACCOUNT,
                    "restartPolicy": "Never",
                    "containers": [
                        {
                            "name": "ai-trader",
                            "image": f"{ECR_REPO}:trader-latest",
                            "args": _base_args(body),
                            "envFrom": [{"configMapRef": {"name": "ai-trader-config"}}],
                        }
                    ],
                }
            },
        },
    }


def _build_replay_job(body, message_id):
    name = _job_name("replay", body["date"], message_id)
    shard_count = int(body.get("shardCount") or 1)
    # messageId 기반 run-id — 여러 샤드(파드)가 하나의 Indexed Job으로 뜨므로
    # 전부 이 name(=run-id로도 씀)을 공유한다. FR-19의 "샤드마다 같은 run-id를
    # 줘야 세션 그룹이 맞는다"는 요구사항을 Job 이름 자체로 자동 충족한다.
    args = _base_args(body) + [
        f"-run-id={name}",
        "-shard-index=$(JOB_COMPLETION_INDEX)",
        f"-shard-count={shard_count}",
    ]
    if body.get("fromTs"):
        args.append(f"-from-ts={body['fromTs']}")
    if body.get("toTs"):
        args.append(f"-to-ts={body['toTs']}")

    return {
        "apiVersion": "batch/v1",
        "kind": "Job",
        "metadata": {"name": name, "namespace": REPLAY_NAMESPACE},
        "spec": {
            "backoffLimit": 0,
            "ttlSecondsAfterFinished": 3600,
            # completionMode=Indexed가 파드마다 JOB_COMPLETION_INDEX 환경변수를
            # 자동으로 채워준다(K8s 1.24+ 표준 기능) — replayengine의 -shard-index를
            # 그 값으로 그대로 채우면(K8s가 args의 $(VAR) 형태를 컨테이너 자신의
            # 환경변수로 치환해줌) 별도 조율 없이 파드별로 서로 다른 샤드를 맡는다.
            "completions": shard_count,
            "parallelism": shard_count,
            "completionMode": "Indexed",
            "template": {
                "spec": {
                    "serviceAccountName": REPLAY_SERVICE_ACCOUNT,
                    "restartPolicy": "Never",
                    "containers": [
                        {
                            "name": "replay-engine",
                            "image": f"{ECR_REPO}:replayengine-latest",
                            "args": args,
                            "envFrom": [{"configMapRef": {"name": "replay-config"}}],
                        }
                    ],
                }
            },
        },
    }


def _cluster_info():
    if not _cluster_cache:
        cluster_name = os.environ["EKS_CLUSTER_NAME"]
        resp = _eks_client.describe_cluster(name=cluster_name)
        cluster = resp["cluster"]
        _cluster_cache["name"] = cluster_name
        _cluster_cache["endpoint"] = cluster["endpoint"]
        _cluster_cache["ca_data"] = cluster["certificateAuthority"]["data"]
    return _cluster_cache["name"], _cluster_cache["endpoint"], _cluster_cache["ca_data"]


def _bearer_token(cluster_name):
    # aws-iam-authenticator/`aws eks get-token`과 동일한 방식이다: STS
    # GetCallerIdentity 프리사인 URL을 "k8s-aws-v1." 접두사 + base64url(패딩
    # 제거)로 감싸면 EKS가 받아들이는 bearer 토큰이 된다. x-k8s-aws-id 헤더로
    # 어느 클러스터를 대상으로 서명했는지 명시한다(클러스터마다 토큰이 달라야
    # 하므로 이 헤더가 서명에 포함돼야 함) — eks:DescribeCluster와는 별개로,
    # 이 토큰 자체는 순수 STS 서명이라 추가 EKS 권한이 필요 없다.
    session = botocore.session.get_session()
    region = _sts_client.meta.region_name
    signer = RequestSigner(
        service_id=_sts_client.meta.service_model.service_id,
        region_name=region,
        signing_name="sts",
        signature_version="v4",
        credentials=session.get_credentials(),
        event_emitter=session.get_component("event_emitter"),
    )
    params = {
        "method": "GET",
        "url": f"https://sts.{region}.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15",
        "body": {},
        "headers": {"x-k8s-aws-id": cluster_name},
        "context": {},
    }
    signed_url = signer.generate_presigned_url(
        params, region_name=region, expires_in=STS_TOKEN_EXPIRES_SECONDS, operation_name="",
    )
    token = base64.urlsafe_b64encode(signed_url.encode("utf-8")).decode("utf-8").rstrip("=")
    return "k8s-aws-v1." + token


def _create_job(namespace, manifest):
    cluster_name, endpoint, ca_data = _cluster_info()
    token = _bearer_token(cluster_name)

    # urllib은 CA 파일 경로가 필요해 임시 파일로 내려쓴다 — Lambda의 /tmp는
    # 실행 환경 안에서 쓰기 가능한 유일한 디렉터리다.
    with tempfile.NamedTemporaryFile(mode="wb", suffix=".crt", delete=False) as ca_file:
        ca_file.write(base64.b64decode(ca_data))
        ca_path = ca_file.name
    ssl_ctx = ssl.create_default_context(cafile=ca_path)

    url = f"{endpoint}/apis/batch/v1/namespaces/{namespace}/jobs"
    req = urllib.request.Request(
        url,
        data=json.dumps(manifest).encode("utf-8"),
        method="POST",
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, context=ssl_ctx, timeout=K8S_REQUEST_TIMEOUT_SECONDS) as resp:
            return resp.status, json.loads(resp.read().decode("utf-8") or "{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode("utf-8") or "{}")
