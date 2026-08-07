package main

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config는 trader가 연결할 외부 서비스 주소를 담습니다. backend/config.Load(),
// orderapi/config.go의 LoadConfig()와 같은 패턴 — .env가 없어도(prod) 오류로 취급하지 않습니다.
type Config struct {
	BackendURL  string
	OrderAPIURL string
}

// LoadConfig는 로컬의 .env 파일(있으면)을 읽어들인 뒤, 환경변수 기반 설정을 반환합니다.
// BACKEND_URL/ORDERAPI_URL은 기본값을 두지 않고 필수로 요구합니다 — 환경마다 다른
// 값인데(로컬은 localhost, 실제 배포 환경은 클러스터 내부 서비스 주소) 없을 때 조용히
// localhost로 넘어가면 배포 환경에서는 그 자리에서 에러가 나는 게 아니라 한참 뒤
// 연결 시도에서야 실패가 드러납니다 — orderapi/config.go의 KAFKA_BROKER와 같은 이유.
func LoadConfig() Config {
	if err := godotenv.Load(); err != nil {
		log.Printf("'.env' 파일을 찾지 못했습니다 (prod 환경이라면 정상): %v", err)
	}

	backendURL := os.Getenv("BACKEND_URL")
	if backendURL == "" {
		log.Fatal("BACKEND_URL 환경변수가 필요합니다.")
	}

	orderAPIURL := os.Getenv("ORDERAPI_URL")
	if orderAPIURL == "" {
		log.Fatal("ORDERAPI_URL 환경변수가 필요합니다.")
	}

	return Config{BackendURL: backendURL, OrderAPIURL: orderAPIURL}
}
