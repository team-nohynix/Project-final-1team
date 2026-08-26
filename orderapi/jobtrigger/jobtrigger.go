// Package jobtrigger는 trader/replayengine 실행을 요청하는 "시작" 트리거를
// SQS로 발행합니다. orderapi는 K8s Job 생성 권한을 직접 갖지 않습니다 —
// job-trigger Lambda(infra/lambda/job-trigger/index.py)가 이 큐를 소비해
// 실제 K8s Job을 만듭니다(infra/job-trigger.tf, infra/irsa.tf의 sa-ingest-api
// 정책 주석 "Job 생성 RBAC은 여기 부여하지 않는다 — 공격 표면 축소" 참고).
package jobtrigger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Request는 POST /v1/jobs의 요청 본문이자, 그대로 SQS 메시지 바디로 발행되는
// 값입니다 — Lambda가 이 JSON을 그대로 파싱해 K8s Job manifest를 만듭니다.
// 필드는 trader/replayengine의 CLI 플래그와 1:1로 대응합니다
// (docs/deployment-env-vars.md 참고). ShardCount는 replay 전용(ai-trader는 샤딩
// 개념 자체가 없음)이지만, FromTS/ToTS(FR-27 구간 지정)는 2026-08-25부터
// ai-trader(trader)도 지원합니다(trader/replay.filterEventRange 참고) — 둘 다
// jobType으로 구분하는 쪽(Lambda의 _base_args)이 처리하므로 여기서 jobType별로
// 필드를 나누지 않습니다 — 이 struct 하나가 두 잡타입의 상위집합입니다.
type Request struct {
	JobType     string   `json:"jobType"` // "ai-trader" | "replay"
	Date        string   `json:"date"`    // YYYY-MM-DD
	Speed       *float64 `json:"speed,omitempty"`
	OrderBucket string   `json:"orderBucket,omitempty"`
	ShardCount  *int     `json:"shardCount,omitempty"` // replay 전용, 1 이상
	FromTS      *int64   `json:"fromTs,omitempty"`     // ai-trader/replay 공통, Unix ms
	ToTS        *int64   `json:"toTs,omitempty"`       // ai-trader/replay 공통, Unix ms
}

// ValidateRequest는 순수 검증 로직입니다(테스트 용이성을 위해 SQS 발행과 분리).
// jobType/date만 구조적으로 검증합니다 — speed/orderBucket 등은 trader/
// replayengine 자신이 이미 검증하는 값들이라 여기서 다시 강하게 검증할
// 필요는 없고, 명백히 잘못된 것만 조기에 걸러냅니다.
func ValidateRequest(req Request) (errorCode, message string, ok bool) {
	switch req.JobType {
	case "ai-trader", "replay":
	default:
		return "INVALID_JOB_TYPE", "jobType은 ai-trader 또는 replay만 가능합니다.", false
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		return "INVALID_DATE", "date는 YYYY-MM-DD 형식이어야 합니다.", false
	}
	if req.Speed != nil && *req.Speed <= 0 {
		return "INVALID_SPEED", "speed는 0보다 커야 합니다.", false
	}
	if req.ShardCount != nil && *req.ShardCount < 1 {
		return "INVALID_SHARD_COUNT", "shardCount는 1 이상이어야 합니다.", false
	}
	return "", "", true
}

// Publisher는 Request를 어딘가에 발행합니다. 핸들러는 이 인터페이스만 알고
// 실제 구현(SQSPublisher)은 몰라도 됩니다 — orderapi/kafkaclient.Publisher와
// 같은 테스트 패턴(가짜 구현체 주입 가능, 실제 SQS 없이 핸들러 테스트).
type Publisher interface {
	Publish(ctx context.Context, req Request) error
}

// SQSPublisher는 team1-sqs-job-trigger 큐(infra/job-trigger.tf)에 발행하는
// 실제 구현체입니다.
type SQSPublisher struct {
	client   *sqs.Client
	queueURL string
}

// NewSQSPublisher는 AWS SDK v2 기본 자격증명 체인(EKS IRSA)으로 SQS 클라이언트를
// 만듭니다. 리전은 명시적으로 지정하지 않습니다 — S3/Bedrock/MSK IAM 코드와
// 같은 이유로 SDK가 AWS_REGION/IRSA에서 자동으로 찾습니다.
func NewSQSPublisher(ctx context.Context, queueURL string) (*SQSPublisher, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("AWS 설정 로드 실패: %w", err)
	}
	return &SQSPublisher{client: sqs.NewFromConfig(awsCfg), queueURL: queueURL}, nil
}

// Publish는 req를 JSON으로 직렬화해 SQS 메시지 본문으로 보냅니다.
func (p *SQSPublisher) Publish(ctx context.Context, req Request) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("작업 요청 직렬화 실패: %w", err)
	}
	_, err = p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(p.queueURL),
		MessageBody: aws.String(string(body)),
	})
	return err
}

// HTTPPublisher는 2026-08-25 홈서버 이전용 SQSPublisher 대체 구현체입니다 —
// SQS/Lambda 대신, 같은 Docker Compose 네트워크 안의 job-trigger 서비스
// (homelab/job-trigger)에 그대로 JSON을 POST합니다. job-trigger가 이
// Request를 받아 trader/replayengine 이미지를 `docker run`으로 직접
// 실행합니다 — job-trigger Lambda(infra/lambda/job-trigger/index.py)가
// SQS를 소비해 K8s Job을 만들던 것과 같은 역할, 다른 실행 수단.
type HTTPPublisher struct {
	url    string
	client *http.Client
}

// NewHTTPPublisher는 url(예: http://job-trigger:9000/v1/jobs)에 그대로 POST할
// Publisher를 만듭니다. job-trigger는 신뢰된 내부 네트워크 전용이라 인증이
// 없습니다 — orderapi 자신의 POST /v1/jobs 핸들러가 이미 입력을 검증하므로
// (ValidateRequest), 여기서 다시 검증하지 않습니다.
func NewHTTPPublisher(url string) *HTTPPublisher {
	return &HTTPPublisher{url: url, client: &http.Client{Timeout: 10 * time.Second}}
}

func (p *HTTPPublisher) Publish(ctx context.Context, req Request) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("작업 요청 직렬화 실패: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("작업 트리거 요청 생성 실패: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("작업 트리거 요청 실패: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("작업 트리거가 %d를 반환함", resp.StatusCode)
	}
	return nil
}
