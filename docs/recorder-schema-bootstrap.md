# recorder DB 스키마 최초 적용

`recorder`는 스키마를 자동으로 만들지 않는다(마이그레이션 툴 없음, 이 repo의 컨벤션 — `CLAUDE.md` 참고). RDS(`team1-truss-db`, MySQL 8.4)에 딱 한 번, 아래 명령으로 손으로 적용하면 된다. 자동화(K8s Job 등)는 만들지 않기로 했다 — 한 번만 하는 작업에 굳이 유지보수 부담을 지는 상용 파이프라인을 붙일 필요가 없다는 판단.

## 사전 준비

- `kubectl`이 `team1-eks`에 붙어 있어야 함(이 글 작성 시점 기준 `a-student-13`만 접근 권한 있음).
- `jq` 설치돼 있어야 함.
- 이 저장소를 클론해서 `recorder/schema.sql`을 로컬에 갖고 있어야 함(또는 `docker run --rm --entrypoint cat 727646470302.dkr.ecr.ap-northeast-2.amazonaws.com/team1-truss:recorder-latest /app/schema.sql > schema.sql`로 이미지에서 꺼내도 됨).

## ⚠️ 실행 전에 반드시 확인할 것

아래 명령의 **`<recorder DB 이름>`을 실제 값으로 바꿔야 한다.** 이 값은 `recorder`를 배포할 때 쓰는 `DATABASE_URL` 환경변수의 데이터베이스 이름 부분과 **정확히 같아야 한다**(`user:pass@tcp(host:port)/그 이름?parseTime=true&loc=UTC`). RDS 클러스터 자체에는 기본 데이터베이스가 없어서(`aws rds describe-db-clusters`로 확인, `DatabaseName: None`) 이름을 직접 정해서 만들어야 한다.

## 실행

```bash
# 1. RDS 자격증명 가져오기 — VPC 접근 불필요, AWS CLI만 있으면 됨
# (RDS가 manage_master_user_password=true라 비밀번호를 Secrets Manager가 관리함)
SECRET=$(aws secretsmanager get-secret-value \
  --secret-id "arn:aws:secretsmanager:ap-northeast-2:727646470302:secret:rds!cluster-08fdff0d-8a26-4284-83ed-6c06c9136490-kqpVdW" \
  --query SecretString --output text)
DB_USER=$(echo "$SECRET" | jq -r .username)
DB_PASS=$(echo "$SECRET" | jq -r .password)

# 2. 데이터베이스 생성 — <recorder DB 이름>을 실제 이름으로 바꿔서 실행
kubectl run mysql-client --rm -i --restart=Never --namespace=backend --image=mysql:8.4 \
  --command -- mysql -h team1-truss-db.cluster-cnqmcq6uwqa3.ap-northeast-2.rds.amazonaws.com \
  -u "$DB_USER" -p"$DB_PASS" -e "CREATE DATABASE IF NOT EXISTS <recorder DB 이름>;"

# 3. 스키마 적용 — 여기도 <recorder DB 이름>을 2번과 똑같이 바꿔서 실행
kubectl run mysql-client --rm -i --restart=Never --namespace=backend --image=mysql:8.4 \
  --command -- mysql -h team1-truss-db.cluster-cnqmcq6uwqa3.ap-northeast-2.rds.amazonaws.com \
  -u "$DB_USER" -p"$DB_PASS" <recorder DB 이름> < recorder/schema.sql
```

`kubectl run ... -i ... < recorder/schema.sql`는 로컬 파일 내용을 그대로 파드 안 `mysql` 프로세스의 표준입력으로 흘려보낸다 — 로컬에서 `mysql ... < schema.sql`을 실행하는 것과 결과가 같다. 파드는 `--rm`으로 실행 후 자동 삭제된다.

## 참고

- RDS 엔드포인트/시크릿 ARN은 2026-08-11 기준 실제 값(Terraform state + `aws rds describe-db-clusters`로 확인). RDS를 재생성하면 바뀔 수 있음.
- `mysql:8.4` 이미지는 RDS(8.4.10)와 버전을 맞춘 것 — 실제 크게 중요하지 않지만 굳이 다른 버전을 쓸 이유도 없음.
- `schema.sql`은 `CREATE TABLE IF NOT EXISTS`/`INSERT ... ON DUPLICATE KEY UPDATE` + (2026-08-20부터) 존재 여부를 먼저 확인하는 `CALL create_index_if_absent(...)`로만 되어 있어 재실행해도 안전함(멱등) — **2026-08-20 이전 버전은 이 문장이 사실이 아니었다**: 인덱스들이 가드 없는 평범한 `CREATE INDEX`라 두 번째 실행이 `ERROR 1061: Duplicate key name`으로 즉시 실패했다(직접 재현해서 확인). 지금은 디스포저블 Docker MySQL로 첫 적용/재적용 둘 다 검증 완료.

## 2026-08-20: `idx_trade_order_mode_submitted` 인덱스 추가 — 실 서비스 RDS에 적용 필요 (시급)

`recorder/query.OrderSummary`/`UnresolvedOrders`(세션 종료 시 미종결 주문 자동 정리, `CLAUDE.md`의 "Session-end unresolved-order cleanup" 참고)가 `trade_order`를 커버하는 인덱스 없이 매번 풀스캔하고 있었다. 세션 정리가 세션 종료마다 자동으로 도는 기능이 되면서(`RECORDER_URL` 배선 완료, 2026-08-19/20) 이 풀스캔이 반복 트리거돼 **RDS 라이터 인스턴스 CPU가 13:09 KST부터 지금까지 계속 ~99%에 붙어있는 실제 장애**로 이어졌다(`aws cloudwatch get-metric-statistics --namespace AWS/RDS --metric-name CPUUtilization --dimensions Name=DBInstanceIdentifier,Value=team1-truss-db-instance-1`로 확인 — Aurora는 `DBClusterIdentifier`로 조회하면 데이터가 안 나오고, 라이터 인스턴스의 `DBInstanceIdentifier`로 조회해야 함).

`recorder/schema.sql`이 이미 고쳐져 있고(위 스키마 적용 방식 그대로 재실행하면 됨), 위에서 확인했듯 이제 재실행해도 안전하다. **실 RDS에 이 파일을 다시 적용하는 것만 남았다** — 위 "실행" 섹션의 3번 명령을 그대로 다시 실행하면 된다(같은 `<recorder DB 이름>`으로). 새로 생기는 테이블/기존 인덱스는 전부 건드리지 않고 `idx_trade_order_mode_submitted`만 새로 추가된다.

(참고: 같은 날 추가된 `idx_trade_order_status`는 Jaden Yang이 이미 실 RDS에 직접 무중단 적용을 끝내둔 상태라, 이 재적용에서는 `create_index_if_absent`가 "이미 있음"으로 보고 건너뛴다 — 새로 생기는 건 `idx_trade_order_mode_submitted` 하나뿐이라는 뜻.)

```bash
# 1. RDS 자격증명 가져오기
SECRET=$(aws secretsmanager get-secret-value \
  --secret-id "arn:aws:secretsmanager:ap-northeast-2:727646470302:secret:rds!cluster-08fdff0d-8a26-4284-83ed-6c06c9136490-kqpVdW" \
  --query SecretString --output text)
DB_USER=$(echo "$SECRET" | jq -r .username)
DB_PASS=$(echo "$SECRET" | jq -r .password)

# 2. 스키마 재적용 — <recorder DB 이름>을 실제 값으로 바꿔서 실행
#    (schema.sql 전체를 다시 흘려보내도 안전함 — 테이블은 CREATE TABLE IF NOT EXISTS,
#    인덱스는 CALL create_index_if_absent(...)라 이미 있는 건 전부 건너뛰고
#    idx_trade_order_mode_submitted만 새로 생성된다)
kubectl run mysql-client --rm -i --restart=Never --namespace=backend --image=mysql:8.4 \
  --command -- mysql -h team1-truss-db.cluster-cnqmcq6uwqa3.ap-northeast-2.rds.amazonaws.com \
  -u "$DB_USER" -p"$DB_PASS" <recorder DB 이름> < recorder/schema.sql

# 3. 확인 — idx_trade_order_mode_submitted가 생겼는지
kubectl run mysql-client --rm -i --restart=Never --namespace=backend --image=mysql:8.4 \
  --command -- mysql -h team1-truss-db.cluster-cnqmcq6uwqa3.ap-northeast-2.rds.amazonaws.com \
  -u "$DB_USER" -p"$DB_PASS" <recorder DB 이름> \
  -e "SHOW INDEX FROM trade_order WHERE Key_name='idx_trade_order_mode_submitted';"
```

적용 직후 CloudWatch `CPUUtilization`을 몇 분 지켜보면 인덱스가 실제로 효과가 있었는지(스캔 비용이 줄어 CPU가 내려가는지) 바로 확인할 수 있다.
