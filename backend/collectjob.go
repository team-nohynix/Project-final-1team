package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"backend/dataset"
)

// collectStatus는 collectJob.Status가 가질 수 있는 값입니다.
type collectStatus string

const (
	collectStatusInProgress collectStatus = "IN_PROGRESS"
	collectStatusCompleted  collectStatus = "COMPLETED"
)

// collectJob은 POST /v1/collect 요청 하나의 진행 상태입니다. 실제 수집(20개
// 마켓 전체, 하루치)은 업비트 rate limit 때문에 몇 분씩 걸릴 수 있어서
// (실측 193초) CloudFront의 오리진 응답 대기 한계(180초, 늘릴 수 없는 값)를
// 넘길 수 있습니다 — 그래서 요청을 받는 즉시 202로 이 job을 돌려주고, 실제
// 수집은 백그라운드에서 진행한 뒤 GET /v1/collect/{jobId}로 상태를 조회하는
// 구조로 바꿨습니다(2026-08-12, 팀원이 실제 배포에서 504를 재현하고 제안한
// 설계). Results는 완료 전까지는 비어 있습니다.
type collectJob struct {
	JobID   string          `json:"jobId"`
	Date    string          `json:"date"`
	Range   dataset.Range   `json:"range"`
	Status  collectStatus   `json:"status"`
	Results []CollectResult `json:"results,omitempty"`
}

// collectJobStore는 진행 중인/완료된 수집 작업을 메모리에만 보관합니다 —
// orderapi/idempotency.Store와 같은 이유로 재시작하면 사라져도 괜찮습니다
// (수집 작업 자체가 하루 단위로 드문 일이고, 재시작 후엔 그냥 다시 요청하면
// 됨 — 영속화할 만한 가치가 없음).
type collectJobStore struct {
	mu   sync.Mutex
	jobs map[string]*collectJob
}

func newCollectJobStore() *collectJobStore {
	return &collectJobStore{jobs: make(map[string]*collectJob)}
}

// create는 새 job을 IN_PROGRESS 상태로 등록하고 그 복사본을 반환합니다.
func (s *collectJobStore) create(date string, r dataset.Range) collectJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := &collectJob{JobID: newJobID(), Date: date, Range: r, Status: collectStatusInProgress}
	s.jobs[job.JobID] = job
	return *job
}

// complete는 백그라운드 수집이 끝난 뒤 결과를 채우고 상태를 COMPLETED로 바꿉니다.
func (s *collectJobStore) complete(jobID string, results []CollectResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[jobID]; ok {
		job.Status = collectStatusCompleted
		job.Results = results
	}
}

// get은 job의 현재 상태 복사본을 반환합니다 — 호출부가 락 밖에서 안전하게
// 읽을 수 있게 값(포인터 아님)으로 돌려줍니다.
func (s *collectJobStore) get(jobID string) (collectJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return collectJob{}, false
	}
	return *job, true
}

// newJobID는 orderapi/session.newSessionID와 같은 패턴(crypto/rand 기반
// hex)입니다 — 순번 카운터가 아닌 이유도 같습니다: 여러 요청이 동시에 들어와도
// 충돌 없이 서로 다른 ID가 나와야 합니다.
func newJobID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("job_%d", time.Now().UnixNano())
	}
	return "job_" + hex.EncodeToString(buf)
}
