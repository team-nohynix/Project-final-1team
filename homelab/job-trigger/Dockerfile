# kubeadm 이전(2026-08-26) 이후 client-go로 in-cluster K8s API를 직접 부르는
# 방식으로 바뀌어서 Docker 소켓/CLI가 더 이상 필요 없다 — 다른 Go 서비스들과
# 같은 표준 alpine 멀티스테이지로 되돌린다(Docker-outside-of-Docker 시절의
# docker:27-cli 베이스는 git 이력 참고).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/job-trigger .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
COPY --from=build /out/job-trigger /app/job-trigger
USER appuser
EXPOSE 9000
ENTRYPOINT ["/app/job-trigger"]
