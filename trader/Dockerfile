# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/trader .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
WORKDIR /app
# orderapi/Dockerfile과 같은 이유(2026-08-25) — WORKDIR 없으면 orderstore.
# NewLocalFileStorage("orders")가 /orders를 만들려다 permission denied.
# 홈서버에서는 orderapi와 같은 order-records-data 볼륨을 여기 마운트해서
# (job-trigger가 docker run -v로 붙임) 두 컨테이너가 같은 주문 기록을 공유한다
# — AWS에서는 둘 다 같은 S3 버킷을 썼던 것의 대체.
RUN mkdir -p /app/orders && chown -R appuser:appuser /app/orders
COPY --from=build /out/trader /app/trader
USER appuser
ENTRYPOINT ["/app/trader"]
