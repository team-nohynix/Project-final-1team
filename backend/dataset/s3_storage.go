package dataset

import (
	"bytes"
	"context"
	"encoding/json"
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

// team1RegionName은 team1-truss-market-data 버킷이 존재하는 리전입니다.
// 이 프로젝트는 항상 ap-northeast-2에서만 동작하므로 고정값으로 둡니다.
const team1RegionName = "ap-northeast-2"

// s3Storage는 JSON 파일을 AWS S3에 저장합니다 (prod 환경 기본값).
// 자격증명은 AWS SDK의 기본 자격증명 체인을 그대로 사용합니다 — 로컬에서는
// 환경변수/공유 프로파일, EC2에서는 인스턴스 프로파일(IAM 역할)이 자동으로 적용됩니다.
type s3Storage struct {
	bucket string
	client *s3.Client
}

// NewS3Storage는 bucket에 파일을 저장하는 Storage를 반환합니다.
func NewS3Storage(bucket string) Storage {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(team1RegionName))
	if err != nil {
		log.Fatalf("AWS 설정 로드 실패: %v", err)
	}

	return &s3Storage{
		bucket: bucket,
		client: s3.NewFromConfig(cfg),
	}
}

func (s *s3Storage) SaveBatch(b BatchFile, start, end time.Time) (string, error) {
	return s.putIfAbsent(b, b.Market, start, end, "batch")
}

func (s *s3Storage) SaveStream(st StreamFile, start, end time.Time) (string, error) {
	return s.putIfAbsent(st, st.Market, start, end, "stream")
}

func (s *s3Storage) LoadBatch(market string, start, end time.Time) (BatchFile, error) {
	var b BatchFile
	err := s.getJSON(market, start, end, "batch", &b)
	return b, err
}

func (s *s3Storage) LoadStream(market string, start, end time.Time) (StreamFile, error) {
	var st StreamFile
	err := s.getJSON(market, start, end, "stream", &st)
	return st, err
}

// putIfAbsent는 CLAUDE.md의 멱등성 설계대로, 같은 키의 파일이 이미 있으면
// 재생성/재업로드 없이 기존 파일을 그대로 서빙합니다.
func (s *s3Storage) putIfAbsent(v any, market string, start, end time.Time, kind string) (string, error) {
	ctx := context.Background()
	key := s.objectKey(market, start, end, kind)

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return s.uri(key), nil
	}

	var notFound *types.NotFound
	if !errors.As(err, &notFound) {
		return "", fmt.Errorf("S3 HeadObject 확인 실패: %w", err)
	}

	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("JSON 직렬화 실패: %w", err)
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return "", fmt.Errorf("S3 업로드 실패: %w", err)
	}

	return s.uri(key), nil
}

// getJSON은 S3에서 객체를 읽어 v에 언마샬합니다. 객체가 없으면 ErrNotFound를 반환합니다.
func (s *s3Storage) getJSON(market string, start, end time.Time, kind string, v any) error {
	ctx := context.Background()
	key := s.objectKey(market, start, end, kind)

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return ErrNotFound
		}
		return fmt.Errorf("S3 GetObject 실패: %w", err)
	}
	defer out.Body.Close()

	raw, err := io.ReadAll(out.Body)
	if err != nil {
		return fmt.Errorf("S3 응답 읽기 실패: %w", err)
	}

	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("JSON 파싱 실패: %w", err)
	}

	return nil
}

// objectKey는 SaveXxx/LoadXxx가 공유하는 S3 키 생성 규칙입니다.
func (s *s3Storage) objectKey(market string, start, end time.Time, kind string) string {
	return fmt.Sprintf("%s/%s_%s_%s.json", market, formatFileTime(start), formatFileTime(end), kind)
}

func (s *s3Storage) uri(key string) string {
	return fmt.Sprintf("s3://%s/%s", s.bucket, key)
}
