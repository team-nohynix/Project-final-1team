// Package session은 "트레이더/시뮬레이터는 동시에 하나만 실행돼야 한다"는 팀
// 결정(2026-08-06)을 강제하는 배타적 세션 락입니다 — 두 개 이상의 트레이더, 또는
// 트레이더와 리플레이 엔진이 동시에 실행되면 같은 매칭 엔진 호가창에 서로 다른
// 실행의 주문이 섞여 들어가는 상황(undefined로 결론)을 막습니다.
//
// 세션은 실행이 시작될 때 딱 한 번만 클레임합니다 — 주문 하나하나가 오가는
// POST /v1/orders 경로에는 이 패키지가 전혀 관여하지 않으므로 NFR-01(초당
// 10,000건) 처리량에 영향이 없습니다. 클레임 후에는 하트비트로 TTL을 갱신하고,
// 끝나면 명시적으로 반납합니다 — 크래시로 반납을 못 하면 TTL이 지나 자동으로
// 풀립니다(자기치유, matching/kafkaclient의 컨슈머 그룹 세션 타임아웃과 같은 설계
// 철학).
//
// FR-19(리플레이 엔진 분산 실행, 2026-08-07 팀 결정)를 위해 배타성의 단위가
// "프로세스 1개"에서 "실행 그룹(runID) 1개"로 확장됐습니다 — 같은 리플레이
// 실행에 속한 여러 `replayengine` 샤드가 같은 runID로 Claim하면 전부 같은
// 그룹에 합류하고, 서로 다른 runID(또는 owner)는 지금처럼 409로 거절됩니다.
// runID를 안 주면(트레이더처럼 애초에 여러 프로세스로 나뉠 일이 없는 경우)
// 서버가 하나 생성해서 "멤버 1개짜리 그룹"으로 취급 — 예전 동작과 완전히
// 동일합니다.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// sessionActiveGauge/sessionStartedAtGauge는 Grafana에서 "지금 실행 중인가",
// "경과 시간"을 바로 패널로 뽑기 위한 지표입니다(2026-08-21) — session_events_total
// (orderapi/sessionhandlers.go)은 카운터라 "지금 상태"를 못 주고, FR-19 리플레이
// 샤딩 때문에 Claim 호출 자체도 매번 "새 실행 시작"은 아닙니다(그룹에 합류하는
// 멤버마다 호출됨). 그래서 claimScript가 "그룹을 새로 만들었다"(n==1)고 알려줄
// 때만, releaseScript가 "마지막 멤버였다"(n==2)고 알려줄 때만 이 게이지를
// 갱신합니다 — LastRun의 IN_PROGRESS/종료 판정과 정확히 같은 기준입니다.
// session_started_at_timestamp는 유닉스 초라서 Grafana에서 time() - 이 값으로
// 바로 경과시간을 계산할 수 있습니다.
var (
	sessionActiveGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "session_active",
		Help: "지금 진행 중인 실행(페이퍼 트레이딩/리플레이)이 있으면 1, 없으면 0",
	})
	sessionStartedAtGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "session_started_at_timestamp",
		Help: "현재(또는 가장 최근) 실행이 시작된 유닉스 타임스탬프(초) — 경과시간은 time() - 이 값",
	})
	// sessionOwnerKindGauge는 2026-08-24 추가 — Grafana "최근 실행 종류" 패널이
	// 페이퍼 트레이딩(owner=trader)과 리플레이(owner=replayengine)를 구분해
	// 보여줄 수 있게 합니다. sessionStartedAtGauge와 같은 이유로 Release에서는
	// 안 건드립니다 — "지금 진행 중이거나 가장 최근에 있었던 실행이 뭐였는지"를
	// 세션이 끝나도 계속 보여주기 위함입니다. 값은 Grafana 값 매핑으로 텍스트로
	// 바꿉니다: 0=기록 없음, 1=페이퍼 트레이딩(trader), 2=리플레이(replayengine).
	sessionOwnerKindGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "session_owner_kind",
		Help: "현재(또는 가장 최근) 실행의 종류 — 0=없음, 1=trader(페이퍼 트레이딩), 2=replayengine(리플레이)",
	})
)

func init() {
	prometheus.MustRegister(sessionActiveGauge, sessionStartedAtGauge, sessionOwnerKindGauge)
}

// ownerKind는 sessionOwnerKindGauge 값 규칙(위 주석)을 owner 문자열에서 계산합니다.
func ownerKind(owner string) float64 {
	switch owner {
	case "trader":
		return 1
	case "replayengine":
		return 2
	default:
		return 0
	}
}

// activeKey는 지금 활성 그룹의 runID만 담습니다(배타성 보장은 이 키 하나로만
// 이루어집니다 — 아래 claimScript/heartbeatScript/releaseScript가 전부 이 키를
// 원자적으로 비교-후-변경합니다). metaKey는 활성 그룹의 보조 정보(마지막으로
// 합류한 멤버가 누구인지, 언제 합류했는지)를 담아 충돌 메시지를 사람이 읽기
// 좋게 만드는 용도뿐입니다 — activeKey와 원자적으로 묶여 있지 않아도 안전합니다
// (정확성은 activeKey만으로 보장됨). membersKeyPrefix+runID는 그 그룹에 지금
// 합류해 있는 멤버 목록(Redis Set)입니다 — Release가 "내가 이 그룹의 마지막
// 멤버인가"를 판단해서, 마지막 멤버가 반납할 때만 activeKey를 즉시 지우기
// 위한 용도입니다(그래야 트레이더 1개짜리 그룹의 기존 "반납하면 바로 다음
// 실행이 시작 가능" 동작이 그대로 유지됩니다). sessionIDSep로 이어붙인
// "{runID}{sep}{memberID}"를 클라이언트에 opaque한 sessionId로 돌려줘서,
// Heartbeat/Release가 어느 그룹의 어느 멤버인지 그 문자열 하나로 알아낼 수
// 있게 합니다.
const (
	activeKey        = "orderapi:session:active"
	metaKey          = "orderapi:session:meta"
	membersKeyPrefix = "orderapi:session:members:"
	sessionIDSep     = "."

	// lastRunKey는 activeKey/metaKey와 달리 반납(Release) 시 지워지지 않습니다 —
	// "지금 뭔가 실행 중인가"가 아니라 "가장 최근 실행이 어떻게 끝났는가"를
	// 프론트가 실행이 끝난 뒤에도 계속 조회할 수 있어야 하기 때문입니다
	// (2026-08-12, 프론트의 "페이퍼 트레이딩 결과 화면" 지원 — 실행 상태/시작·
	// 종료 시각/오류 메시지). 다음 Claim이 새 그룹을 만들 때 덮어씁니다. 이
	// 프로젝트의 세션 배타성 보장(activeKey 하나만 원자적으로 다룸) 덕분에
	// 언제든 활성 실행은 최대 1개뿐이라, 이 키를 잠금 스크립트 밖에서 별도
	// GET/SET으로 다뤄도 두 실행이 동시에 이 키를 다르게 쓸 일이 없습니다.
	lastRunKey = "orderapi:session:lastrun"

	// prevRunKey는 lastRunKey 바로 이전 실행의 기록입니다(2026-08-19, 주문재생
	// "부하 시나리오 미리보기" 화면의 "직전 실행과 비교" 지원). lastRunKey는
	// 슬롯이 하나뿐이라 새 실행이 시작되는 순간 그 값이 곧바로 덮어써지는데,
	// "지금 진행 중인 실행"과 "그 직전 실행"을 화면에 동시에 보여주려면 새
	// 실행이 lastRunKey를 덮어쓰기 전에 그 값을 여기로 한 칸 밀어둬야 합니다.
	// activeKey와 마찬가지로 반납 시 지워지지 않고, 다음다음 Claim이 새 그룹을
	// 만들 때만 다시 밀려납니다.
	prevRunKey = "orderapi:session:prevrun"

	// stopKeyPrefix+runID는 "이 그룹은 정지 요청됨" 플래그입니다(2026-08-20,
	// 프론트 "중지" 버튼 지원). 활성 그룹의 exclusivity를 보장하는
	// activeKey/membersKey와 달리 원자적 스크립트로 묶여있지 않습니다 —
	// 이 값은 정확성에 관여하지 않는(있어도 그만, 없어도 그만인) 순수
	// 신호값이라 GET/SET을 따로 해도 안전합니다. TTL을 세션과 같은 값으로
	// 둬서, 하트비트가 이 값을 한 번도 못 보고 그룹이 먼저 죽는 극단적인
	// 경우에도 self-healing으로 알아서 사라집니다(이 프로젝트 전반의
	// "TTL이 정확성의 최종 안전장치" 철학과 동일).
	stopKeyPrefix = "orderapi:session:stop:"
)

func stopKey(runID string) string { return stopKeyPrefix + runID }

// RunStatus 값 — 프론트가 그대로 표시할 수 있는 4가지뿐입니다. STOPPED는
// 2026-08-20에 추가됐습니다(사용자가 "중지" 버튼으로 실행을 직접 중단시킨
// 경우 — FAILED와 구분해야 프론트가 "에러로 죽었다"와 "사용자가 멈췄다"를
// 다르게 표시할 수 있습니다).
const (
	RunStatusInProgress = "IN_PROGRESS"
	RunStatusCompleted  = "COMPLETED"
	RunStatusFailed     = "FAILED"
	RunStatusStopped    = "STOPPED"
)

// RunOutcome은 Release 호출부(trader/replayengine)가 "이번 실행이 어떻게
// 끝났는지"를 함께 보고할 때 씁니다. Message는 실패 사유(에러 메시지) 또는
// 성공이어도 남길 만한 요약(예: 일부 마켓 실패)을 자유 형식으로 담습니다 —
// 비워도 됩니다.
type RunOutcome struct {
	Status  string
	Message string
}

// RunRecord는 GET(마지막 실행 결과 조회)의 응답 데이터입니다. Speed는
// 2026-08-20 추가 — 페이퍼 트레이딩 "실행 결과" 화면에 실행 배속을 같이
// 보여달라는 요청 지원(다른 숫자들, 특히 미체결 건수를 해석하는 데 필수적인
// 맥락이라 우선순위 높게 추가함). trader/replayengine이 -speed 플래그로
// 이미 알고 있는 값을 Claim 시점에 실어 보낸다 — 실행 도중 안 바뀌는 값이라
// 시작 시점에 한 번만 기록하면 충분하다.
//
// Date는 2026-08-24 추가 — "시스템 종합 현황" 대시보드의 "주문 유실"
// 지표(예정된 주문량 vs 실제 접수량) 지원. replayengine이 -date 플래그로
// 재생한 날짜를 실어 보낸다(어느 날짜의 기록 파일을 재생했는지는 그 실행이
// 끝난 뒤에도 대시보드가 GET /v1/jobs/replay-preview?date=...를 다시 조회할
// 수 있어야 알 수 있는데, RunRecord 자체엔 그때까지 이 값이 없었음). trader가
// 보낸 -date는 시장 데이터 재생 날짜일 뿐 FR-17 기록 파일의 "예정된 주문량"
// 개념과 무관해서(생성된 주문 수는 여전히 관측 불가 — CLAUDE.md 참고) 비워
// 보낸다 — 이 필드는 replayengine 실행에서만 의미가 있다.
type RunRecord struct {
	RunID     string
	Owner     string
	Status    string
	StartedAt time.Time
	EndedAt   time.Time // Status가 IN_PROGRESS면 zero value
	Message   string
	Speed     float64
	Date      string
}

func membersKey(runID string) string { return membersKeyPrefix + runID }

// splitSessionID는 Claim이 발급한 "{runID}{sep}{memberID}" 형태의 opaque
// sessionId를 다시 분리합니다. 구분자가 없으면(다른 형식의 값이 들어온 경우)
// ok=false — Heartbeat/Release는 이걸 "활성 세션 아님"으로 취급합니다.
//
// sessionIDSep이 "#"가 아니라 "."인 이유(실제 라이브 검증 중 발견) —
// trader/session·replayengine/session의 Heartbeat/Release는 sessionID를 URL
// 인코딩 없이 그냥 문자열로 이어붙여 경로를 만듭니다(`.../v1/sessions/`+sessionID+`/heartbeat`).
// "#"는 URL 프래그먼트 구분자라 curl/HTTP 클라이언트가 그 뒤를 통째로 잘라
// 서버에 안 보내버려서("/heartbeat"까지 잘려나감), 실제로 PUT 요청이 엉뚱한
// 경로로 나가 405가 나는 걸 직접 겪었습니다. "."는 URL 경로에서 별도 인코딩
// 없이 안전하고, `newSessionID()`가 만드는 hex 문자열(0-9a-f)과도 절대
// 겹치지 않아 안전한 구분자입니다.
func splitSessionID(sessionID string) (runID, memberID string, ok bool) {
	idx := strings.LastIndex(sessionID, sessionIDSep)
	if idx < 0 {
		return "", "", false
	}
	return sessionID[:idx], sessionID[idx+1:], true
}

// ErrNotActive는 주어진 sessionID가 지금 활성 세션이 아닐 때(만료됐거나, 처음부터
// 존재한 적이 없거나, 다른 세션으로 교체됐을 때) 반환됩니다.
var ErrNotActive = errors.New("해당 세션은 현재 활성 상태가 아닙니다")

// Info는 클레임 성공 결과 또는(충돌 시) 현재 활성 세션에 대한 정보입니다.
// SessionID는 이 특정 호출(멤버)에 발급된 opaque ID(Heartbeat/Release에 씀),
// RunID는 그 멤버가 속한 그룹의 ID입니다 — 단독 실행(runID 미지정)이면 서버가
// 생성한 값이 SessionID/RunID 양쪽에 다 쓰이지만, 값 자체는 다릅니다(SessionID는
// "{runID}.{memberID}" 합성값). 충돌 조회(currentInfo)에서는 SessionID를 채울
// 방법이 없어(활성 키엔 runID만 있음) 비워둡니다.
type Info struct {
	SessionID string
	RunID     string
	Owner     string
	ClaimedAt time.Time
	TTL       time.Duration
}

// ConflictError는 이미 다른 세션이 활성 상태일 때 Claim이 반환합니다.
type ConflictError struct {
	Current Info
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("이미 활성 세션이 있습니다 (owner=%s, claimedAt=%s)", e.Current.Owner, e.Current.ClaimedAt.Format(time.RFC3339))
}

// Store는 세션 클레임/하트비트/반납을 다룹니다. RedisStore가 실제 구현이고,
// 이 인터페이스는 orderapi의 HTTP 핸들러를 실제 Redis 없이 테스트하기 위해
// 존재합니다(orderapi/kafkaclient.Publisher와 같은 패턴).
type Store interface {
	// Claim은 owner 이름으로 runID 그룹에 합류합니다(runID가 비어있으면 새
	// 그룹을 만들어 그 안에서 단독 멤버가 됩니다). 이미 다른 runID/owner의
	// 그룹이 활성 상태면 *ConflictError를 반환합니다. speed는 그룹을 새로
	// 만드는 호출(=그 실행의 첫 멤버)일 때만 RunRecord에 기록됩니다 — 이미
	// 활성인 그룹에 합류하는 멤버(리플레이 샤드 2번째 이후)가 보낸 값은
	// 무시합니다(같은 실행이면 모든 샤드가 같은 speed를 쓰는 게 당연하므로).
	// date도 같은 규칙(첫 멤버 값만 기록) — RunRecord.Date 참고.
	Claim(ctx context.Context, owner, runID, date string, speed float64) (Info, error)
	// Heartbeat는 세션 TTL을 갱신하면서, 이 그룹에 정지 요청이 들어와 있는지도
	// 같이 확인합니다(2026-08-20, RequestStop 참고) — 호출부(trader/replayengine의
	// RunHeartbeat)가 이미 이 주기로 서버와 왕복하고 있으므로, 별도 폴링 루프
	// 없이 이 왕복에 얹어서 신호를 전달합니다.
	Heartbeat(ctx context.Context, sessionID string) (stopRequested bool, err error)
	// RequestStop은 runID 그룹에 정지 신호를 보냅니다. 그 그룹이 지금 활성
	// 상태가 아니면(이미 끝났거나 존재한 적 없으면) ErrNotActive입니다. 실제
	// 정지는 즉시 일어나지 않고, 그 그룹의 다음 하트비트 왕복(최악의 경우
	// TTL/3, 기본값 기준 약 10초) 때 반영됩니다 — 정지 요청 자체는 프로세스를
	// 직접 건드리지 않는 순수 신호이기 때문입니다(하트비트가 이미 폴링하고
	// 있는 것을 재사용).
	RequestStop(ctx context.Context, runID string) error
	// Release는 이 멤버를 그룹에서 반납합니다. outcome은 그룹의 마지막
	// 멤버가 반납할 때만(=이 실행 전체가 끝날 때만) LastRun에 반영됩니다 —
	// 리플레이 샤드처럼 여러 멤버 중 일부만 먼저 끝나는 경우 아직 실행 중인
	// 것으로 남아야 합니다. finalized는 이번 반납이 그 "마지막 멤버"였는지를
	// 알려줍니다 — 호출부(releaseSessionHandler)가 세션 종료 시점에만 해야
	// 하는 후처리(미종결 주문 정리, 2026-08-19)를 트리거하는 데 씁니다.
	// finalized=true일 때만 record가 채워집니다(그 세션의 owner/시작·종료
	// 시각 — 정리 로직이 mode/구간을 계산하는 데 필요).
	Release(ctx context.Context, sessionID string, outcome RunOutcome) (record RunRecord, finalized bool, err error)
	// LastRun은 가장 최근 실행의 기록을 반환합니다. 지금까지 한 번도 실행된
	// 적이 없으면 found=false(에러 아님).
	LastRun(ctx context.Context) (RunRecord, bool, error)
	// PreviousRun은 LastRun 바로 이전 실행의 기록을 반환합니다("직전 실행과
	// 비교" 지원). 실행이 2번 미만이었으면 found=false(에러 아님).
	PreviousRun(ctx context.Context) (RunRecord, bool, error)
	// AppendLastRunNote는 runID가 지금 LastRun 슬롯을 차지하고 있는 경우에만
	// 그 RunRecord.Message에 note를 덧붙입니다(2026-08-20, 세션 종료 자동
	// 정리가 매칭 엔진 랙으로 건너뛰었을 때 그 사실을 "실행 결과" 화면에도
	// 보이게 하려는 용도) — runID가 이미 다른 실행으로 넘어갔으면(그 사이 새
	// 실행이 시작됨) 아무것도 안 하고 에러 없이 반환합니다(그 새 실행의
	// 메시지를 실수로 건드리면 안 되므로).
	AppendLastRunNote(ctx context.Context, runID, note string) error
}

// RedisStore는 Store를 Redis로 구현합니다.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisStore는 ttl마다 하트비트로 갱신돼야 하는 세션 락을 만듭니다.
func NewRedisStore(client *redis.Client, ttl time.Duration) *RedisStore {
	return &RedisStore{client: client, ttl: ttl}
}

type metaRecord struct {
	Owner     string    `json:"owner"`
	ClaimedAt time.Time `json:"claimedAt"`
}

// claimScript는 "활성 그룹이 없으면 새로 만들고, 있는데 내 runID와 같으면
// 합류(TTL 갱신), 다른 runID/owner의 그룹이면 거절"을 원자적으로 처리합니다.
// GET 따로, SETNX/EXPIRE 따로 하면 그 사이에 다른 요청이 끼어들 여지가
// 생기므로(예: 막 만료된 키를 두 클레임이 동시에 보고 각자 다르게 판단),
// 하나의 스크립트로 묶었습니다 — heartbeatScript/releaseScript가 이미 쓰던
// "원자적 비교-후-실행" 원칙과 같습니다.
const claimScript = `
local current = redis.call('GET', KEYS[1])
if current == false then
	redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
	return 1
elseif current == ARGV[1] then
	redis.call('EXPIRE', KEYS[1], ARGV[2])
	return 2
end
return 0
`

// Claim은 owner 이름으로 runID 그룹에 합류합니다. runID가 비어있으면(트레이더
// 등 원래부터 한 프로세스로만 도는 호출자) 서버가 하나 생성해 "멤버 1개짜리
// 그룹"을 만듭니다 — 이 경우 예전(그룹 개념 도입 전) 동작과 완전히 동일합니다.
func (s *RedisStore) Claim(ctx context.Context, owner, runID, date string, speed float64) (Info, error) {
	now := time.Now().UTC()
	if runID == "" {
		runID = newSessionID()
	}

	res, err := s.client.Eval(ctx, claimScript, []string{activeKey}, runID, int(s.ttl.Seconds())).Result()
	if err != nil {
		return Info{}, fmt.Errorf("세션 클레임 실패: %w", err)
	}
	n, _ := res.(int64)
	if n == 0 {
		return Info{}, &ConflictError{Current: s.currentInfo(ctx)}
	}

	// 이 멤버를 그룹의 멤버 목록(Set)에 등록합니다 — Release가 "내가 마지막
	// 멤버인가"를 판단하는 데 씁니다. TTL은 하트비트마다 activeKey와 함께
	// 갱신됩니다(아무도 하트비트를 안 보내면 그룹 전체가 같이 자연 소멸).
	member := newSessionID()
	mKey := membersKey(runID)
	if err := s.client.SAdd(ctx, mKey, member).Err(); err != nil {
		return Info{}, fmt.Errorf("세션 클레임 실패 (멤버 등록): %w", err)
	}
	s.client.Expire(ctx, mKey, s.ttl)

	// meta는 참고 정보뿐이라 만료 시간을 안 둡니다 — 다음 클레임/합류가 성공할
	// 때 그대로 덮어쓰이고, 그룹의 마지막 멤버가 반납할 때 같이 지워집니다.
	if body, err := json.Marshal(metaRecord{Owner: owner, ClaimedAt: now}); err == nil {
		s.client.Set(ctx, metaKey, body, 0)
	}

	// claimScript가 1(새 그룹 생성)을 돌려줬을 때만 LastRun을 새로 씁니다 —
	// 2(기존 그룹에 합류, 예: 리플레이 샤드 2번째 이후)면 이미 첫 멤버가 써둔
	// StartedAt을 건드리면 안 됩니다.
	if n == 1 {
		// lastRunKey를 덮어쓰기 전에, 지금 거기 있는 값(=방금 전까지 "마지막
		// 실행"이었던 것)을 prevRunKey로 한 칸 밀어둡니다 — 이 시점 이후로는
		// 새로 시작하는 이 실행이 lastRunKey를 차지하므로, 그 이전 값을 못
		// 읽게 되기 전에 옮겨야 합니다. claimScript가 이미 activeKey를 원자적으로
		// 선점한 뒤라 이 시점에 다른 Claim이 동시에 새 그룹을 만들 수 없으므로,
		// 별도 Lua 스크립트 없이 plain GET+SET으로도 안전합니다.
		if body, err := s.client.Get(ctx, lastRunKey).Bytes(); err == nil {
			s.client.Set(ctx, prevRunKey, body, 0)
		}

		record := RunRecord{RunID: runID, Owner: owner, Status: RunStatusInProgress, StartedAt: now, Speed: speed, Date: date}
		if body, err := json.Marshal(record); err == nil {
			s.client.Set(ctx, lastRunKey, body, 0)
		}
		sessionActiveGauge.Set(1)
		sessionStartedAtGauge.Set(float64(now.Unix()))
		sessionOwnerKindGauge.Set(ownerKind(owner))
	}

	return Info{
		SessionID: runID + sessionIDSep + member,
		RunID:     runID,
		Owner:     owner,
		ClaimedAt: now,
		TTL:       s.ttl,
	}, nil
}

// heartbeatScript는 "지금 활성 그룹의 runID가 내 runID와 같을 때만" 그룹
// 키와 멤버 목록 키의 TTL을 함께 갱신합니다 — 그 사이 다른 그룹이 클레임했다면
// (예: 내 그룹이 이미 통째로 만료된 뒤) 엉뚱한 그룹을 갱신하지 않습니다.
const heartbeatScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	redis.call('EXPIRE', KEYS[1], ARGV[2])
	redis.call('EXPIRE', KEYS[2], ARGV[2])
	return 1
end
return 0
`

// releaseScript는 이 멤버만 멤버 목록에서 빼고, 그 결과 목록이 비었으면(내가
// 마지막 멤버였으면) 그룹 키/메타까지 같이 지웁니다 — 다른 멤버가 아직
// 남아있으면 그룹은 그대로 살아있습니다(그 멤버들의 하트비트가 계속 TTL을
// 갱신해줌). 멤버 목록에서 빼는 데 실패하면(이미 반납했거나 애초에 없던
// 멤버) ErrNotActive입니다.
const releaseScript = `
local removed = redis.call('SREM', KEYS[2], ARGV[2])
if removed == 0 then
	return 0
end
local remaining = redis.call('SCARD', KEYS[2])
if remaining == 0 and redis.call('GET', KEYS[1]) == ARGV[1] then
	redis.call('DEL', KEYS[1])
	redis.call('DEL', KEYS[3])
	redis.call('DEL', KEYS[2])
	return 2
end
return 1
`

// Heartbeat는 세션(멤버)이 속한 그룹의 TTL을 갱신합니다. sessionID 형식이
// 이상하거나(구분자 없음) 그 그룹이 더는 활성 상태가 아니면 ErrNotActive를
// 반환합니다. TTL 갱신에 성공하면 stopKey(runID)도 같이 확인해 정지 요청
// 여부를 돌려줍니다 — 이 조회는 원자적 스크립트 밖에서 별도 GET으로 하는데,
// stopKey는 정확성에 관여하지 않는 신호값이라(위 stopKeyPrefix 주석 참고)
// TTL 갱신과 같은 트랜잭션으로 묶일 필요가 없습니다. 조회 자체가 실패해도
// (Redis 순간 장애 등) 하트비트 자체를 실패시키지 않고 "정지 요청 없음"으로
// fail-open합니다 — 다음 하트비트 주기에 다시 확인되므로 신호를 완전히
// 놓치지 않습니다.
func (s *RedisStore) Heartbeat(ctx context.Context, sessionID string) (bool, error) {
	runID, _, ok := splitSessionID(sessionID)
	if !ok {
		return false, ErrNotActive
	}
	res, err := s.client.Eval(ctx, heartbeatScript, []string{activeKey, membersKey(runID)}, runID, int(s.ttl.Seconds())).Result()
	if err != nil {
		return false, fmt.Errorf("세션 하트비트 실패: %w", err)
	}
	if n, _ := res.(int64); n == 0 {
		return false, ErrNotActive
	}
	stopRequested, err := s.client.Exists(ctx, stopKey(runID)).Result()
	if err != nil {
		return false, nil
	}
	return stopRequested > 0, nil
}

// RequestStop은 runID 그룹에 정지 신호를 보냅니다. activeKey가 지금 이
// runID를 가리키고 있을 때만 신호를 세팅합니다 — 이미 끝났거나 존재한 적
// 없는 runID에 신호를 세팅해봐야 아무도 못 볼 고아 키만 남기 때문입니다.
func (s *RedisStore) RequestStop(ctx context.Context, runID string) error {
	current, err := s.client.Get(ctx, activeKey).Result()
	if err == redis.Nil || current != runID {
		return ErrNotActive
	}
	if err != nil {
		return fmt.Errorf("정지 요청 실패: %w", err)
	}
	if err := s.client.Set(ctx, stopKey(runID), "1", s.ttl).Err(); err != nil {
		return fmt.Errorf("정지 요청 실패: %w", err)
	}
	return nil
}

// Release는 이 멤버를 그룹에서 명시적으로 반납합니다 — 그룹의 마지막 멤버였을
// 때만 그룹 자체(activeKey)도 즉시 지워지고, 그때만 outcome을 LastRun에
// 반영합니다(리플레이 샤드처럼 아직 남은 멤버가 있으면 이 실행은 안 끝난
// 것이므로 LastRun을 안 건드립니다). sessionID가 이미 반납됐거나 존재한 적
// 없으면 ErrNotActive입니다.
func (s *RedisStore) Release(ctx context.Context, sessionID string, outcome RunOutcome) (RunRecord, bool, error) {
	runID, member, ok := splitSessionID(sessionID)
	if !ok {
		return RunRecord{}, false, ErrNotActive
	}
	res, err := s.client.Eval(ctx, releaseScript, []string{activeKey, membersKey(runID), metaKey}, runID, member).Result()
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("세션 반납 실패: %w", err)
	}
	n, _ := res.(int64)
	if n == 0 {
		return RunRecord{}, false, ErrNotActive
	}
	if n == 2 {
		record := s.finalizeLastRun(ctx, runID, outcome)
		return record, true, nil
	}
	return RunRecord{}, false, nil
}

// finalizeLastRun은 LastRun 레코드에 종료 시각/상태/메시지를 채웁니다. 기존에
// Claim이 써둔 RunID/Owner/StartedAt은 그대로 두고 이어받습니다 — 다만 Redis
// 왕복을 하나 더 아끼려고 그냥 새로 만들어도 되는데(Owner/StartedAt은 outcome
// 호출부가 모르는 값이라 다시 못 채움), 그래서 기존 값을 먼저 읽어와 채웁니다.
// 조회가 실패해도(레코드가 이미 없어졌거나 등) 최소한 상태/메시지는 남깁니다 —
// "완료됐다"는 사실 자체가 "시작 시각을 못 보여준다"보다 훨씬 중요합니다.
func (s *RedisStore) finalizeLastRun(ctx context.Context, runID string, outcome RunOutcome) RunRecord {
	sessionActiveGauge.Set(0)
	record := RunRecord{RunID: runID, Status: outcome.Status, EndedAt: time.Now().UTC(), Message: outcome.Message}
	if body, err := s.client.Get(ctx, lastRunKey).Bytes(); err == nil {
		var existing RunRecord
		if json.Unmarshal(body, &existing) == nil && existing.RunID == runID {
			record.Owner = existing.Owner
			record.StartedAt = existing.StartedAt
			record.Speed = existing.Speed
			record.Date = existing.Date
		}
	}
	if body, err := json.Marshal(record); err == nil {
		s.client.Set(ctx, lastRunKey, body, 0)
	}
	return record
}

// LastRun은 가장 최근 실행 기록을 반환합니다. 한 번도 실행된 적이 없으면
// found=false입니다.
//
// IN_PROGRESS로 저장돼 있어도 그대로 믿지 않습니다 — 그 실행의 활성 락
// (activeKey)이 하트비트 없이 이미 TTL로 자연 만료돼 있으면, Release를 못
// 타고 죽은(크래시했거나 멈춘) 실행으로 보고 여기서 즉시 FAILED로 정리합니다
// (2026-08-25 추가). 이게 없으면 프로세스는 이미 죽어 활성 락도 사라졌는데
// lastRunKey만 영원히 IN_PROGRESS로 남아, 화면은 계속 "실행 중"으로 보이고
// 정작 "중지" 버튼을 누르면 백엔드가 (정확하게) "활성 세션 아님"이라 답하는
// 불일치가 생깁니다 — 실제로 관측된 "중지가 안 먹힌다" 신고의 근본 원인이
// 이것이었습니다. 활성 락이 TTL로 자연 만료된다는 것 자체가 이미 이 패키지의
// 기존 자기치유 원칙("다음 Claim이 그 자리를 자유롭게 가져가도 됨")이므로,
// 그 판단을 LastRun 응답에도 반영하는 건 새 위험이 아니라 이미 성립한 판단을
// 뒤늦게 노출하는 것뿐입니다.
func (s *RedisStore) LastRun(ctx context.Context) (RunRecord, bool, error) {
	body, err := s.client.Get(ctx, lastRunKey).Bytes()
	if err == redis.Nil {
		return RunRecord{}, false, nil
	}
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("마지막 실행 기록 조회 실패: %w", err)
	}
	var record RunRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return RunRecord{}, false, fmt.Errorf("마지막 실행 기록 파싱 실패: %w", err)
	}
	if record.Status == RunStatusInProgress {
		if reconciled, ok := s.reconcileStaleRun(ctx, record); ok {
			return reconciled, true, nil
		}
	}
	return record, true, nil
}

// reconcileStaleRun은 IN_PROGRESS인 record의 활성 락이 이미 사라졌는지
// 확인합니다. 아직 살아있으면(activeKey == record.RunID) ok=false를 돌려줘
// 호출부가 원래 record를 그대로 쓰게 합니다. Redis 조회 자체가 실패하면
// 판단을 유보합니다(fail-open — 다른 백프레셔 신호들과 같은 원칙).
//
// 죽은 것으로 판단되면 finalizeLastRun과 달리 GET+SET을 한 번 더 확인하고
// 씁니다 — AppendLastRunNote와 같은 이유로, 그 사이 새 실행이 이미
// lastRunKey를 넘겨받았을 수 있기 때문입니다(예: 이 runID의 락이 만료된 뒤
// 새 Claim이 성공해 lastRunKey를 새 실행 기록으로 이미 덮어쓴 경우). 그럴 때
// existing.RunID가 record.RunID와 달라지므로, 그 새 실행의 기록을 실수로
// 되돌려쓰지 않도록 아무것도 하지 않고 포기합니다.
func (s *RedisStore) reconcileStaleRun(ctx context.Context, record RunRecord) (RunRecord, bool) {
	current, err := s.client.Get(ctx, activeKey).Result()
	if err == nil && current == record.RunID {
		return RunRecord{}, false // 아직 살아있음 — 손대지 않음
	}
	if err != nil && err != redis.Nil {
		return RunRecord{}, false // Redis 조회 실패 — 판단 유보
	}

	body, err := s.client.Get(ctx, lastRunKey).Bytes()
	if err != nil {
		return RunRecord{}, false
	}
	var existing RunRecord
	if err := json.Unmarshal(body, &existing); err != nil {
		return RunRecord{}, false
	}
	if existing.RunID != record.RunID || existing.Status != RunStatusInProgress {
		// 그 사이 새 실행이 슬롯을 넘겨받았거나, 정상적으로 이미 종료 처리됨 —
		// 손대지 않음.
		return RunRecord{}, false
	}

	finalized := existing
	finalized.Status = RunStatusFailed
	finalized.EndedAt = time.Now().UTC()
	finalized.Message = "세션이 하트비트 없이 만료되어 자동으로 종료 처리되었습니다 (프로세스 응답 없음으로 추정)"
	if newBody, err := json.Marshal(finalized); err == nil {
		s.client.Set(ctx, lastRunKey, newBody, 0)
	}
	sessionActiveGauge.Set(0)
	return finalized, true
}

// AppendLastRunNote는 Store 인터페이스 설명 참고. lastRunKey를 원자적
// 스크립트로 안 다루는 이유는 finalizeLastRun과 같습니다 — 세션 배타적
// 잠금 덕분에 이 함수를 부를 시점엔 활성 실행이 최대 1개뿐이라, GET+SET
// 사이에 다른 실행이 끼어들어 경합할 여지가 실질적으로 없습니다.
func (s *RedisStore) AppendLastRunNote(ctx context.Context, runID, note string) error {
	body, err := s.client.Get(ctx, lastRunKey).Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("마지막 실행 기록 조회 실패: %w", err)
	}
	var record RunRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return fmt.Errorf("마지막 실행 기록 파싱 실패: %w", err)
	}
	if record.RunID != runID {
		// 그 사이 새 실행이 시작돼 lastRunKey를 넘겨받음 — 이 실행의 결과는
		// 더 이상 조회 가능한 슬롯에 없습니다(prevRunKey까지 뒤쫓지는 않음,
		// 흔치 않은 경합이라 v1에선 그냥 넘어감).
		return nil
	}
	if record.Message == "" {
		record.Message = note
	} else {
		record.Message = record.Message + " | " + note
	}
	newBody, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, lastRunKey, newBody, 0).Err()
}

// PreviousRun은 LastRun 바로 이전 실행의 기록을 반환합니다. 지금까지 실행이
// 2번 미만이었으면(한 번도 없었거나, 지금 진행 중인/막 끝난 게 처음 실행이면)
// found=false입니다.
func (s *RedisStore) PreviousRun(ctx context.Context) (RunRecord, bool, error) {
	body, err := s.client.Get(ctx, prevRunKey).Bytes()
	if err == redis.Nil {
		return RunRecord{}, false, nil
	}
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("직전 실행 기록 조회 실패: %w", err)
	}
	var record RunRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return RunRecord{}, false, fmt.Errorf("직전 실행 기록 파싱 실패: %w", err)
	}
	return record, true, nil
}

// currentInfo는 충돌 에러 메시지를 사람이 읽기 좋게 만들기 위한 최선의 노력(best
// effort) 조회입니다 — meta 조회가 실패해도 최소한 RunID는 채워서 돌려줍니다.
// SessionID(멤버 ID)는 활성 키에 안 남아있어 여기서는 채울 수 없습니다.
func (s *RedisStore) currentInfo(ctx context.Context) Info {
	runID, err := s.client.Get(ctx, activeKey).Result()
	if err != nil {
		return Info{}
	}
	info := Info{RunID: runID}

	body, err := s.client.Get(ctx, metaKey).Bytes()
	if err != nil {
		return info
	}
	var m metaRecord
	if err := json.Unmarshal(body, &m); err != nil {
		return info
	}
	info.Owner = m.Owner
	info.ClaimedAt = m.ClaimedAt
	return info
}

func newSessionID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return "sess_" + hex.EncodeToString(buf)
}
