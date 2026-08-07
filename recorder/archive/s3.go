package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// tradeResultsRegion은 team1-truss-trade-results 버킷이 존재할 리전입니다 —
// 이 계정은 항상 ap-northeast-2에서만 동작하므로 backend/trader와 동일하게
// 고정값으로 둡니다.
const tradeResultsRegion = "ap-northeast-2"

// S3Store는 마이크로배치를 S3에 저장합니다 — trader/orderstore.S3Storage와
// 같은 자격증명 방식(AWS SDK 기본 자격증명 체인: 로컬은 환경변수/공유 프로파일,
// EC2/EKS는 인스턴스 프로파일/IRSA). objectKey가 항상 고유하므로 local.go와
// 마찬가지로 HeadObject 사전 확인은 하지 않습니다.
type S3Store struct {
	bucket string
	client *s3.Client
}

func NewS3Store(bucket string) *S3Store {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(tradeResultsRegion))
	if err != nil {
		log.Fatalf("AWS 설정 로드 실패: %v", err)
	}
	return &S3Store{bucket: bucket, client: s3.NewFromConfig(cfg)}
}

func (s *S3Store) Save(kind string, records []any) (string, error) {
	key := objectKey(kind)

	body, err := json.Marshal(records)
	if err != nil {
		return "", fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	_, err = s.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return "", fmt.Errorf("S3 업로드 실패: %w", err)
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}
