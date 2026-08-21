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

volumes:
  mysql-data:
