# recorder DB 스키마 최초 적용

> **2026-08-24 갱신: 이 문서의 수동 적용 절차는 더 이상 필요 없다.** 팀이 RDS(`team1-truss-db`)를 삭제하고 자체 호스팅 MySQL EC2(`team1-mysql`, `i-09f1dca7e19bbd3ff`)로 전환하면서, `recorder/schema.sql`을 `infra/mysql-ec2/`가 EC2 위 컨테이너의 `/docker-entrypoint-initdb.d/`에 올려두게 됐다 — 공식 `mysql` Docker 이미지가 데이터 디렉터리가 비어있는 최초 기동에만 이 디렉터리의 파일을 자동 실행하므로, 아래처럼 `kubectl run`으로 손으로 적용할 필요가 없어졌다(RDS는 이 기동 훅을 제공하지 않아서 손으로 해야 했던 것). 아래 내용은 RDS 시절 절차의 역사적 기록으로만 남겨둔다.

## (참고용, RDS 시절 절차 — 더 이상 실행하지 않음)

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
- `schema.sql`은 `CREATE TABLE IF NOT EXISTS`/`INSERT ... ON DUPLICATE KEY UPDATE`로만 되어 있어 재실행해도 안전함(멱등).
