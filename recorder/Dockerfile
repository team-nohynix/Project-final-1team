# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/recorder .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
COPY --from=build /out/recorder /app/recorder
# schema.sql은 recorder가 런타임에 읽지 않습니다(자동 마이그레이션 없음 —
# CLAUDE.md 참고, RDS에 최초 1회 손으로 적용해야 함) — 그래도 이미지에 같이
# 담아두는 이유는, 배포 담당자가 이 리포지토리를 따로 클론하지 않고 이미지만
# 갖고 있어도 `docker run --rm --entrypoint cat <image> /app/schema.sql`
# 이나 `docker cp`로 꺼내 바로 적용할 수 있게 하기 위함입니다.
COPY --from=build /src/schema.sql /app/schema.sql
USER appuser
EXPOSE 8082
ENTRYPOINT ["/app/recorder"]
