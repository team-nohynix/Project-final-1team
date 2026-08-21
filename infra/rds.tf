# 2026-08-21: team1-truss-db(Multi-AZ MySQL 클러스터, db.m6gd.large=vCPU 2개)를
# 삭제했다 — 50배속 부하테스트에서 recorder 쓰기량이 CPU를 지속적으로 99%까지
# 밀어붙이는 걸 실측했고, 팀 결정으로 자체 호스팅 MySQL EC2(mysql-ec2.tf)로
# 전환하면서 두 DB를 동시에 띄워 이중으로 비용이 나가는 걸 피하려고 기존
# RDS부터 먼저 내렸다. 기존 데이터는 버려도 된다고 확인받은 상태였다
# (deletion_protection=false, skip_final_snapshot=true였어서 스냅샷 없이 삭제됨).
