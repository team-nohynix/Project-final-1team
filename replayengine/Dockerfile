# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/replayengine .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
WORKDIR /app
# orderapi/Dockerfile과 같은 이유(2026-08-25) — orderstore.NewLocalFileStorage("orders")가
# 상대경로라 WORKDIR 없이는 /orders(permission denied)로 간다. 공유
# order-records-data 볼륨은 job-trigger가 docker run -v로 붙인다.
RUN mkdir -p /app/orders && chown -R appuser:appuser /app/orders
COPY --from=build /out/replayengine /app/replayengine
USER appuser
ENTRYPOINT ["/app/replayengine"]
