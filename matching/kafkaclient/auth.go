package kafkaclient

import (
	"crypto/tls"

	kafka "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl/scram"
)

// NewDialer는 kafka.Reader/kafka.ConsumerGroup/kafka.Conn(DialLeader)이 쓰는
// *kafka.Dialer를 만듭니다. username/password가 둘 다 비어있으면 nil을 돌려줍니다 —
// nil Dialer는 kafka-go가 인증 없는 기본 연결로 취급하므로, 로컬 dev-kafka(SASL
// 없음)에서는 아무것도 안 바뀝니다. 둘 다 채워져 있으면 SCRAM-SHA-512+TLS로
// 인증합니다 — MSK가 SASL/SCRAM에 요구하는 조합입니다(AWS_MSK_IAM은 kafka-go에
// 내장 지원이 없고 직접 프로토콜을 구현해야 해서, 검증 없이 넣는 위험을 피하려고
// SCRAM을 택했습니다. CLAUDE.md 참고). matching/rebalance.LoadTracker도 이걸
// 씁니다(직접 kafka.DialLeader를 부르는 자리라 별도로 필요) — 그래서 exported.
func NewDialer(username, password string) (*kafka.Dialer, error) {
	if username == "" || password == "" {
		return nil, nil
	}
	mechanism, err := scram.Mechanism(scram.SHA512, username, password)
	if err != nil {
		return nil, err
	}
	return &kafka.Dialer{SASLMechanism: mechanism, TLS: &tls.Config{}}, nil
}

// NewTransport는 kafka.Writer가 쓰는 버전입니다 — NewDialer와 같은 이유로,
// 인증 정보가 없으면 nil을 돌려줘서 kafka.Writer가 DefaultTransport(인증 없음)를
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
func NewTransport(username, password string) (kafka.RoundTripper, error) {
	if username == "" || password == "" {
		return nil, nil
	}
	mechanism, err := scram.Mechanism(scram.SHA512, username, password)
	if err != nil {
		return nil, err
	}
	return &kafka.Transport{SASL: mechanism, TLS: &tls.Config{}}, nil
}
