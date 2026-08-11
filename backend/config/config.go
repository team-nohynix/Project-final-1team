package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config는 dev/prod 환경에 따라 달라지는 설정값을 담습니다.
type Config struct {
	Env      string // "dev" 또는 "prod"
	S3Bucket string
	Port     string // HTTP 서버 포트, 기본값 "8080"
}

// Load는 로컬의 .env 파일(있으면)을 읽어들인 뒤, 환경변수 기반 설정을 반환합니다.
// prod 배포 환경에는 .env 파일이 없고 대신 환경변수가 직접 주입되므로,
// .env 로드 실패 자체는 오류로 취급하지 않습니다.
//
// APP_ENV는 dev/prod 중 하나로 명시적으로 설정되어 있어야 합니다. 값이 없거나
// 오타 등으로 잘못 설정된 경우, 조용히 dev로 넘어가지 않고 즉시 종료합니다 —
// prod에서 이 값이 잘못 전달되면 데이터가 S3 대신 컨테이너 로컬 디스크에 쓰이다
// 유실될 수 있어서, 잘못된 설정은 그 자리에서 바로 드러나야 합니다.
func Load() Config {
	if err := godotenv.Load(); err != nil {
		log.Printf("'.env' 파일을 찾지 못했습니다 (prod 환경이라면 정상): %v", err)
	}

	env := os.Getenv("APP_ENV")
	if env != "dev" && env != "prod" {
		log.Fatalf("APP_ENV 환경변수가 올바르지 않습니다 (현재 값: %q). \"dev\" 또는 \"prod\"로 설정해야 합니다.", env)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	cfg := Config{
		Env:      env,
		S3Bucket: os.Getenv("S3_BUCKET"),
		Port:     port,
	}

	if cfg.Env == "prod" && cfg.S3Bucket == "" {
		log.Fatalf("prod 환경에서는 S3_BUCKET 환경변수가 필요합니다.")
	}

	return cfg
}
