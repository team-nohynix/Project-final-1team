# recorder DB 스키마 최초 적용

RDS 시절엔 `recorder/schema.sql`을 `kubectl run`으로 손으로 한 번 적용해야 했지만, 지금은
자체 호스팅 MySQL EC2(`team1-mysql`, `infra/mysql-ec2.tf`)로 전환되면서 필요 없어졌다 —
공식 `mysql` Docker 이미지가 데이터 디렉터리가 비어있는 최초 기동에만
`/docker-entrypoint-initdb.d/`의 파일을 자동 실행하는데, `infra/mysql-ec2/docker-compose.yml.tpl`이
`recorder/schema.sql`을 그 경로에 올려둔다.

스키마를 고칠 때는 `recorder/schema.sql`만 수정하면 된다 — `CREATE TABLE IF NOT EXISTS`/
`CALL create_index_if_absent(...)`로 작성돼 있어 재실행해도 안전하다(멱등).

로컬 개발용 `infra/dev-mysql`은 이 자동 적용 메커니즘이 없어 여전히 수동 적용이 필요하다
(`CLAUDE.md`의 Commands 절 참고).
