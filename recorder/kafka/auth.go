package kafka

import (
	"crypto/tls"

	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/scram"
)

// newDialer는 kafka.Reader가 쓰는 *kafka.Dialer를 만듭니다. username/password가
// 둘 다 비어있으면 nil을 돌려줍니다 — kafka.ReaderConfig.Dialer가 nil이면 인증
// 없는 기본 연결을 그대로 쓰므로, 로컬 dev-kafka(SASL 없음)에서는 아무것도 안
// 바뀝니다. 둘 다 채워져 있으면 SCRAM-SHA-512+TLS로 인증합니다 — MSK가
// SASL/SCRAM에 요구하는 조합입니다(AWS_MSK_IAM은 kafka-go에 내장 지원이 없고
// 직접 프로토콜을 구현해야 해서, 검증 없이 넣는 위험을 피하려고 SCRAM을
// 택했습니다. CLAUDE.md 참고).
func newDialer(username, password string) (*kafka.Dialer, error) {
	if username == "" || password == "" {
		return nil, nil
	}
	mechanism, err := scram.Mechanism(scram.SHA512, username, password)
	if err != nil {
		return nil, err
	}
	return &kafka.Dialer{SASLMechanism: mechanism, TLS: &tls.Config{}}, nil
}
