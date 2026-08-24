services:
  mysql:
    image: mysql:8.4
    restart: unless-stopped
    environment:
      MYSQL_ROOT_PASSWORD: ${mysql_root_password}
      MYSQL_DATABASE: team1_truss
    ports:
      - "3306:3306"
    volumes:
      - mysql-data:/var/lib/mysql
      # schema.sql이 여기 있으면 mysql 공식 이미지가 데이터 디렉터리가 비어있는
      # 최초 기동에만 자동 실행한다 — RDS 때처럼 수동 apply할 필요가 없다.
      - /etc/mysql-init:/docker-entrypoint-initdb.d:ro
    command:
      - --max_connections=200
      # 2026-08-24, 실측 튜닝(Grafana 대시보드 "미처리 주문" 백로그 원인 조사
      # 중 발견) — RDS→EC2 이전(infra/rds.tf 참고) 이후 인스턴스 RAM(m6i.2xlarge,
      # 30GiB)에 맞는 InnoDB 튜닝이 안 돼 있었다. RDS는 인스턴스 클래스에 맞춰
      # 파라미터 그룹을 어느 정도 자동으로 잡아주지만, 자체 호스팅 MySQL은
      # 수동으로 안 맞추면 기본값(128MB 버퍼풀 등)을 그대로 쓴다.
      #
      # innodb_buffer_pool_size=22G — 30GiB 중 OS/도커/커넥션(max_connections=200)
      # 여유를 남기고 나머지를 InnoDB 캐시로. 기존 128MB(기본값)에서 실측.
      - --innodb-buffer-pool-size=22G
      # innodb_flush_log_at_trx_commit=2 — 기본값(1)은 커밋마다 redo log를
      # 디스크에 fsync한다. 2로 바꾸면 커밋마다 OS 캐시에만 쓰고 실제 fsync는
      # 1초에 한 번만 한다 — 대신 EC2가 갑자기(OS 크래시 등) 죽으면 최근
      # 1초치 커밋이 유실될 수 있다. 이 DB는 부하테스트/페이퍼트레이딩
      # 전용이라(실거래 아님, 데이터 유실 시 재현 가능) 그 정도 리스크는
      # 감수한다고 팀 결정.
      - --innodb-flush-log-at-trx-commit=2
      # skip-log-bin — mysql:8.4 공식 이미지는 기본적으로 바이너리 로깅이
      # 켜져 있는데(log_bin=ON, sync_binlog=1), 이 DB는 복제/백업/PITR을
      # 전혀 안 쓴다. 켜져 있으면 위 innodb_flush_log_at_trx_commit과는
      # 별개로 커밋마다 binlog까지 한 번 더 fsync한다 — 안 쓰는 기능 때문에
      # 두 번째 fsync 경로가 그냥 남아있던 것이라 꺼서 제거한다.
      - --skip-log-bin

volumes:
  mysql-data:
