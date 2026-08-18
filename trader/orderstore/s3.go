package orderstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"trader/order"
)

// orderRecordsRegion은 주문 기록용 S3 버킷이 존재할 리전입니다.
// 이 계정은 항상 ap-northeast-2에서만 동작하므로 backend와 동일하게 고정값으로 둡니다.
const orderRecordsRegion = "ap-northeast-2"

// S3Storage는 주문 기록 파일을 S3에 저장합니다. 자격증명은 AWS SDK의 기본 자격증명
// 체인을 그대로 사용합니다(backend의 dataset.s3Storage와 동일한 방식) — 로컬에서는
// 환경변수/공유 프로파일, EC2에서는 인스턴스 프로파일이 자동으로 적용됩니다.
//
// 이 버킷은 team1-truss-market-data(시세 데이터용)와 별도이며, 아직 생성되지 않았습니다
// — 생성 및 IAM 정책은 인프라 담당 팀원에게 요청 중입니다. 그 전까지는 main.go가
// NewLocalFileStorage를 기본으로 씁니다.
type S3Storage struct {
	bucket string
	client *s3.Client
}

// NewS3Storage는 bucket에 주문 기록을 저장하는 Storage를 반환합니다.
func NewS3Storage(bucket string) Storage {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(orderRecordsRegion))
	if err != nil {
		log.Fatalf("AWS 설정 로드 실패: %v", err)
	}

	return &S3Storage{bucket: bucket, client: s3.NewFromConfig(cfg)}
}

func (s *S3Storage) Save(market string, start, end time.Time, orders []order.RecordedOrder) (string, error) {
	key := objectKey(market, start, end)

	body, err := json.MarshalIndent(orderRecordFile{Market: market, Range: toRange(start, end), Orders: orders}, "", "  ")
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
