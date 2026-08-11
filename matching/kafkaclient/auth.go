package kafkaclient

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/aws_msk_iam_v2"
)

// NewDialer는 kafka.Reader/kafka.ConsumerGroup/kafka.Conn(DialLeader)이 쓰는
// *kafka.Dialer를 만듭니다. useIAM이 false면 nil을 돌려줍니다 — nil Dialer는
// kafka-go가 인증 없는 기본 연결로 취급하므로, 로컬 dev-kafka(SASL 없음)에서는
// 아무것도 안 바뀝니다. true면 AWS_MSK_IAM(SASL/OAUTHBEARER)+TLS로 인증합니다 —
// MSK Serverless는 IAM 인증만 지원하고 SASL/SCRAM 자체를 지원하지 않는다는 걸
// AWS 공식 문서로 확인해서(이전엔 SCRAM을 택했으나, 그 인프라 전제가 틀렸음이
// 드러남 — CLAUDE.md 참고) 바꿨습니다. 자격증명은 다른 모듈의 S3/Bedrock과
// 같은 AWS SDK v2 기본 체인(EC2 인스턴스 프로파일/EKS IRSA)을 그대로 씁니다.
// matching/rebalance.LoadTracker도 이걸 씁니다(직접 kafka.DialLeader를 부르는
// 자리라 별도로 필요) — 그래서 exported.
func NewDialer(ctx context.Context, useIAM bool) (*kafka.Dialer, error) {
	if !useIAM {
		return nil, nil
	}
	mechanism, err := newIAMMechanism(ctx)
	if err != nil {
		return nil, err
	}
	return &kafka.Dialer{SASLMechanism: mechanism, TLS: &tls.Config{}}, nil
}

// NewTransport는 kafka.Writer가 쓰는 버전입니다 — NewDialer와 같은 이유로,
// useIAM이 false면 nil을 돌려줘서 kafka.Writer가 DefaultTransport(인증 없음)를
// 그대로 쓰게 합니다.
//
// **반환형이 *kafka.Transport가 아니라 kafka.RoundTripper(인터페이스)인 이유**:
// kafka.Writer.Transport 필드가 RoundTripper 인터페이스인데, 여기서 콘크리트
// 타입 *kafka.Transport로 nil을 반환한 뒤 그 nil을 그 필드에 대입하면, "nil
// 포인터를 담은 논nil 인터페이스"가 돼서 w.Transport == nil 검사가 false로
// 나옵니다 — 그러면 kafka-go가 DefaultTransport로 대체하지 않고 그 nil
// *Transport를 그대로 써서 실제 호출 시 패닉이 납니다(로컬에서 인증 없이
// 붙을 때 실제로 이 패닉을 겪고 발견함 — orderapi에서 먼저 재현됨). 함수의
// 반환형 자체를 인터페이스로 선언해 "return nil, nil"이 진짜 nil 인터페이스가
// 되게 해서 해결합니다.
func NewTransport(ctx context.Context, useIAM bool) (kafka.RoundTripper, error) {
	if !useIAM {
		return nil, nil
	}
	mechanism, err := newIAMMechanism(ctx)
	if err != nil {
		return nil, err
	}
	return &kafka.Transport{SASL: mechanism, TLS: &tls.Config{}}, nil
}

// newIAMMechanism은 AWS SDK v2 기본 자격증명/리전 체인으로 aws_msk_iam_v2.Mechanism을
// 만듭니다. 리전은 명시적으로 지정하지 않습니다 — SDK가 AWS_REGION 환경변수/
// EC2 인스턴스 메타데이터/EKS IRSA가 주입한 값을 이미 자동으로 찾으므로, 이
// 코드에 리전을 하드코딩하거나 별도 환경변수를 새로 만들 이유가 없습니다.
func newIAMMechanism(ctx context.Context) (*aws_msk_iam_v2.Mechanism, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("AWS 설정 로드 실패: %w", err)
	}
	return aws_msk_iam_v2.NewMechanism(awsCfg), nil
}
