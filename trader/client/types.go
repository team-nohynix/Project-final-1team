package client

// 아래 타입들은 backend가 반환하는 JSON 응답을 그대로 옮긴 것입니다.
// trader는 backend와 별도 Go 모듈이라 backend/dataset의 타입을 직접 import하지 않고,
// 실제 통신 방식(HTTP+JSON)에 맞춰 이 계약만 따로 들고 있습니다.
// 원본 필드 정의: CLAUDE.md "Output JSON file format", backend/dataset/types.go, backend/server.go.

// ManifestEntry는 한 마켓의 batch/stream 파일을 받아올 수 있는 URL입니다.
type ManifestEntry struct {
	Market    string `json:"market"`
	BatchURL  string `json:"batchUrl"`
	StreamURL string `json:"streamUrl"`
}

// Manifest는 GET /v1/markets/data의 응답입니다.
type Manifest struct {
	Date    string          `json:"date"`
	Markets []ManifestEntry `json:"markets"`
}

// Range는 데이터가 커버하는 기간입니다.
type Range struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// CandleOHLCV는 batch/stream 공통으로 쓰이는 정규화된 캔들 필드입니다.
type CandleOHLCV struct {
	TS     int64   `json:"ts"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// BatchCandles는 일/주/월/년 단위 캔들을 단위별로 묶어 담습니다.
type BatchCandles struct {
	Days   []CandleOHLCV `json:"days"`
	Weeks  []CandleOHLCV `json:"weeks"`
	Months []CandleOHLCV `json:"months"`
	Years  []CandleOHLCV `json:"years"`
}

// BatchFile은 GET /v1/markets/{market}/batch의 응답입니다.
type BatchFile struct {
	Market  string       `json:"market"`
	Range   Range        `json:"range"`
	Candles BatchCandles `json:"candles"`
}

// StreamEvent는 스트림 파일의 이벤트 하나(초/분봉 또는 개별 체결)입니다.
type StreamEvent struct {
	Type   string  `json:"type"`
	TS     int64   `json:"ts"`
	Open   float64 `json:"open,omitempty"`
	High   float64 `json:"high,omitempty"`
	Low    float64 `json:"low,omitempty"`
	Close  float64 `json:"close,omitempty"`
	Volume float64 `json:"volume,omitempty"`
	Price  float64 `json:"price,omitempty"`
	Side   string  `json:"side,omitempty"`
}

// StreamFile은 GET /v1/markets/{market}/stream의 응답입니다. events는 ts 오름차순입니다.
type StreamFile struct {
	Market string        `json:"market"`
	Range  Range         `json:"range"`
	Events []StreamEvent `json:"events"`
}
