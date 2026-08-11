package main

import (
	"log"
	"net/http"

	"backend/config"
	"backend/dataset"
)

func main() {
	cfg := config.Load()
	storage := newStorage(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/collect", collectHandler(storage))
	mux.HandleFunc("GET /v1/markets/data", manifestHandler())
	mux.HandleFunc("GET /v1/markets/{market}/{kind}", fileHandler(storage))

	addr := ":" + cfg.Port
	log.Printf("서버 시작: %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// newStorage는 환경(cfg.Env)에 따라 로컬 디스크 저장소(dev) 또는 S3 저장소(prod)를 선택합니다.
func newStorage(cfg config.Config) dataset.Storage {
	if cfg.Env == "prod" {
		return dataset.NewS3Storage(cfg.S3Bucket)
	}
	return dataset.NewLocalStorage("data")
}
