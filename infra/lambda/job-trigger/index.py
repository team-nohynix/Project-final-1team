"""team1-lambda-job-trigger — placeholder.

Terraform이 최초 배포에 필요한 자리표시자 코드만 담는다. 실제 로직(SQS 메시지의 job_type으로
ai-trader/replay Job 매니페스트를 골라 EKS 프라이빗 엔드포인트에 적용하는 것)은 애플리케이션
코드로 별도 구현해 CI/CD가 `aws lambda update-function-code`로 갱신한다.
"""

import json
import logging

logger = logging.getLogger()
logger.setLevel(logging.INFO)


def handler(event, context):
    for record in event.get("Records", []):
        body = json.loads(record.get("body", "{}"))
        logger.info("job-trigger stub received job_type=%s (not yet implemented)", body.get("job_type"))
    return {"status": "stub"}
