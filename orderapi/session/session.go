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

	"github.com/redis/go-redis/v9"
)

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
)

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
	// 그룹이 활성 상태면 *ConflictError를 반환합니다.
	Claim(ctx context.Context, owner, runID string) (Info, error)
	Heartbeat(ctx context.Context, sessionID string) error
	Release(ctx context.Context, sessionID string) error
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
	return 1
end
return 0
`

// Claim은 owner 이름으로 runID 그룹에 합류합니다. runID가 비어있으면(트레이더
// 등 원래부터 한 프로세스로만 도는 호출자) 서버가 하나 생성해 "멤버 1개짜리
// 그룹"을 만듭니다 — 이 경우 예전(그룹 개념 도입 전) 동작과 완전히 동일합니다.
func (s *RedisStore) Claim(ctx context.Context, owner, runID string) (Info, error) {
	now := time.Now().UTC()
	if runID == "" {
		runID = newSessionID()
	}

	res, err := s.client.Eval(ctx, claimScript, []string{activeKey}, runID, int(s.ttl.Seconds())).Result()
	if err != nil {
		return Info{}, fmt.Errorf("세션 클레임 실패: %w", err)
	}
	if n, _ := res.(int64); n == 0 {
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
end
return 1
`

// Heartbeat는 세션(멤버)이 속한 그룹의 TTL을 갱신합니다. sessionID 형식이
// 이상하거나(구분자 없음) 그 그룹이 더는 활성 상태가 아니면 ErrNotActive를
// 반환합니다.
func (s *RedisStore) Heartbeat(ctx context.Context, sessionID string) error {
	runID, _, ok := splitSessionID(sessionID)
	if !ok {
		return ErrNotActive
	}
	res, err := s.client.Eval(ctx, heartbeatScript, []string{activeKey, membersKey(runID)}, runID, int(s.ttl.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("세션 하트비트 실패: %w", err)
	}
	if n, _ := res.(int64); n == 0 {
		return ErrNotActive
	}
	return nil
}

// Release는 이 멤버를 그룹에서 명시적으로 반납합니다 — 그룹의 마지막 멤버였을
// 때만 그룹 자체(activeKey)도 즉시 지워집니다(다른 멤버가 남아있으면 그룹은
// 안 건드림). sessionID가 이미 반납됐거나 존재한 적 없으면 ErrNotActive입니다.
func (s *RedisStore) Release(ctx context.Context, sessionID string) error {
	runID, member, ok := splitSessionID(sessionID)
	if !ok {
		return ErrNotActive
	}
	res, err := s.client.Eval(ctx, releaseScript, []string{activeKey, membersKey(runID), metaKey}, runID, member).Result()
	if err != nil {
		return fmt.Errorf("세션 반납 실패: %w", err)
	}
	if n, _ := res.(int64); n == 0 {
		return ErrNotActive
	}
	return nil
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
