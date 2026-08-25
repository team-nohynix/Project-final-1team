# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/backend .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
WORKDIR /app
# WORKDIR을 안 잡으면(예전 버전) 컨테이너 cwd가 /라서, 로컬 스토리지 폴백이
# 상대경로 "data"를 만들 때 실제로는 /data를 건드리게 되고(compose의
# /app/data 볼륨 마운트와 무관한 위치), 거기다 appuser가 /에 쓰기 권한이
# 없어 "mkdir data: permission denied"로 죽는다(2026-08-25 홈서버 실측 —
# 온디맨드 수집 500의 진짜 원인, 업비트 rate limit 때문이 아니었음).
# data 디렉터리를 미리 appuser 소유로 만들어둬야 신규 named volume이 처음
# 마운트될 때(빈 볼륨을 이미지 쪽 디렉터리 소유권으로 초기화하는 Docker 동작)
# appuser가 쓸 수 있는 상태로 시작한다.
RUN mkdir -p /app/data && chown -R appuser:appuser /app/data
COPY --from=build /out/backend /app/backend
USER appuser
EXPOSE 8080
ENTRYPOINT ["/app/backend"]
