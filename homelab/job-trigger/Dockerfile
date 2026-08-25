# job-trigger는 호스트 Docker 소켓을 통해 형제 컨테이너(trader/replayengine)를
# 띄워야 하므로, docker CLI가 설치된 이미지가 필요하다 — 다른 서비스들의
# scratch급 alpine 최종 이미지(Dockerfile 패턴)와 다른 이유.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/job-trigger .

FROM docker:27-cli
COPY --from=build /out/job-trigger /app/job-trigger
EXPOSE 9000
ENTRYPOINT ["/app/job-trigger"]
