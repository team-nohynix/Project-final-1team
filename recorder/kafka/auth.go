package kafka

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/aws_msk_iam_v2"
)

// newDialer는 kafka.Reader가 쓰는 *kafka.Dialer를 만듭니다. useIAM이 false면
// nil을 돌려줍니다 — kafka.ReaderConfig.Dialer가 nil이면 인증 없는 기본 연결을
// 그대로 쓰므로, 로컬 dev-kafka(SASL 없음)에서는 아무것도 안 바뀝니다. true면
// AWS_MSK_IAM(SASL/OAUTHBEARER)+TLS로 인증합니다 — MSK Serverless는 IAM 인증만
// 지원하고 SASL/SCRAM 자체를 지원하지 않는다는 걸 AWS 공식 문서로 확인해서
// (이전엔 SCRAM을 택했으나, 그 인프라 전제가 틀렸음이 드러남 — CLAUDE.md 참고)
// 바꿨습니다. 자격증명은 다른 모듈의 S3와 같은 AWS SDK v2 기본 체인(EC2 인스턴스
// 프로파일/EKS IRSA)을 그대로 씁니다.
func newDialer(ctx context.Context, useIAM bool) (*kafka.Dialer, error) {
	if !useIAM {
		return nil, nil
	}
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("AWS 설정 로드 실패: %w", err)
	}
	mechanism := aws_msk_iam_v2.NewMechanism(awsCfg)
	return &kafka.Dialer{SASLMechanism: mechanism, TLS: &tls.Config{}}, nil
}
