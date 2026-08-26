package orderrecords

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// orderRecordsRegion은 주문 기록용 S3 버킷(team1-truss-order-records)이 있는
// 리전입니다. 이 계정은 항상 ap-northeast-2에서만 동작하므로 다른 모듈과
// 동일하게 고정값으로 둡니다.
const orderRecordsRegion = "ap-northeast-2"

// S3Storage는 주문 기록 파일을 S3에서 읽습니다. 자격증명은 AWS SDK의 기본
// 자격증명 체인을 그대로 사용합니다(trader/orderstore, replayengine/orderstore와
// 동일한 방식 — 로컬은 환경변수/공유 프로파일, EKS는 IRSA).
type S3Storage struct {
	bucket string
	client *s3.Client
}

// NewS3Storage는 bucket에서 주문 기록을 읽는 Storage를 반환합니다.
func NewS3Storage(bucket string) Storage {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(orderRecordsRegion))
	if err != nil {
		log.Fatalf("AWS 설정 로드 실패: %v", err)
	}

	return &S3Storage{bucket: bucket, client: s3.NewFromConfig(cfg)}
}

func (s *S3Storage) Load(market string, start, end time.Time) ([]RecordedOrder, error) {
	key := objectKey(market, start, end)

	out, err := s.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("S3 다운로드 실패: %w", err)
	}
	defer out.Body.Close()

	body, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("S3 응답 읽기 실패: %w", err)
	}

	orders, err := decodeOrderRecordFile(body)
	if err != nil {
		return nil, fmt.Errorf("JSON 파싱 실패: %w", err)
	}
	return orders, nil
}

func (s *S3Storage) ListDates() ([]string, error) {
	dates := make(map[string]bool)
	var continuationToken *string
	for {
		out, err := s.client.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, fmt.Errorf("S3 목록 조회 실패: %w", err)
		}
		for _, obj := range out.Contents {
			if obj.Key == nil {
				continue
			}
			if date, ok := parseDateFromObjectKey(*obj.Key); ok {
				dates[date] = true
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		continuationToken = out.NextContinuationToken
	}
	return sortedDatesDesc(dates), nil
}
