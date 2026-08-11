// Package jobtrigger는 trader/replayengine 실행을 요청하는 "시작" 트리거를
// SQS로 발행합니다. orderapi는 K8s Job 생성 권한을 직접 갖지 않습니다 —
// job-trigger Lambda(infra/lambda/job-trigger/index.py)가 이 큐를 소비해
// 실제 K8s Job을 만듭니다(infra/job-trigger.tf, infra/irsa.tf의 sa-ingest-api
// 정책 주석 "Job 생성 RBAC은 여기 부여하지 않는다 — 공격 표면 축소" 참고).
package jobtrigger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Request는 POST /v1/jobs의 요청 본문이자, 그대로 SQS 메시지 바디로 발행되는
// 값입니다 — Lambda가 이 JSON을 그대로 파싱해 K8s Job manifest를 만듭니다.
// 필드는 trader/replayengine의 CLI 플래그와 1:1로 대응합니다
// (docs/deployment-env-vars.md 참고). ShardCount/FromTS/ToTS는 replay 전용이지만
// jobType으로 구분하는 쪽(Lambda)이 무시하면 되므로 여기서 jobType별로 필드를
// 나누지 않습니다 — 이 struct 하나가 두 잡타입의 상위집합입니다.
type Request struct {
	JobType     string   `json:"jobType"` // "ai-trader" | "replay"
	Date        string   `json:"date"`    // YYYY-MM-DD
	Speed       *float64 `json:"speed,omitempty"`
	OrderBucket string   `json:"orderBucket,omitempty"`
	ShardCount  *int     `json:"shardCount,omitempty"` // replay 전용, 1 이상
	FromTS      *int64   `json:"fromTs,omitempty"`     // replay 전용, Unix ms
	ToTS        *int64   `json:"toTs,omitempty"`       // replay 전용, Unix ms
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
