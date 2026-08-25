# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/orderapi .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
WORKDIR /app
# backend/Dockerfile과 같은 이유(2026-08-25) — WORKDIR이 없으면 cwd가 /라서
# orderrecords.NewLocalFileStorage("orders")가 상대경로로 만드는 디렉터리가
# /orders(= 홈서버 docker-compose의 orderapi-data 볼륨 마운트 위치인
# /app/orders와 무관)가 된다. orders 디렉터리를 미리 appuser 소유로 만들어
# named volume 최초 마운트 시 그 소유권으로 초기화되게 한다.
RUN mkdir -p /app/orders && chown -R appuser:appuser /app/orders
COPY --from=build /out/orderapi /app/orderapi
USER appuser
EXPOSE 8081
ENTRYPOINT ["/app/orderapi"]
