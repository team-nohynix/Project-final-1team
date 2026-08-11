# 정적 바이너리 + distroless — Fargate 이미지 풀 시간 단축 목적(레이어를 최소화).
FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/collector .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/collector /collector
USER nonroot:nonroot
ENTRYPOINT ["/collector"]
