package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"backend/dataset"
	"backend/upbit"
)

// collectRequest는 POST /v1/collect의 요청 본문입니다.
type collectRequest struct {
	Date string `json:"date"` // YYYY-MM-DD (KST 기준 하루)
}

// parseDate는 "YYYY-MM-DD"를 KST(한국 표준시) 자정 기준으로 파싱합니다.
// 업비트가 한국 거래소라 팀 결정으로 날짜 경계를 KST로 맞춥니다 — UTC였다면
// 요청한 날짜와 실제 수집 구간이 9시간 밀렸을 것입니다.
func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, upbit.KST)
}

// collectHandler는 POST /v1/collect를 처리합니다. 20개 마켓 전체를 실제로
// 수집하면(하루치, 업비트 rate limit 때문에) 몇 분씩 걸릴 수 있어서(실측
// 193초) 요청을 그 자리에서 기다리지 않습니다 — 202로 collectJob(진행 상태)을
// 즉시 돌려주고, 실제 수집은 백그라운드 고루틴에서 진행합니다. 완료 여부는
// GET /v1/collect/{jobId}로 조회합니다(2026-08-12, CloudFront의 오리진 응답
// 대기 한계 180초를 넘겨 504가 나던 문제 — 이 한계는 CloudFront 쪽에서 늘릴
// 수 없어 응답 패턴 자체를 바꿔야 했습니다. 프론트는 아직 이 변경에 맞춰
// 수정되지 않았습니다 — docs/frontend-backend-integration.md 참고).
//
// collect는 실제로 20개 마켓을 수집하는 함수입니다(운영 중엔 collectAllMarkets,
// main.go가 그대로 넘겨줌) — 실제 업비트 API를 부르는 함수를 주입 가능하게
// 만들어서, 테스트에서는 네트워크 없이 즉시 끝나는 가짜 함수를 넣을 수
// 있습니다(collectAllMarkets/collectMarket 자체는 여전히 주입 불가능한
// 상태지만, 이 핸들러가 그걸 직접 부르지 않게 한 단계 분리했습니다).
func collectHandler(storage dataset.Storage, jobs *collectJobStore, collect func(dataset.Storage, time.Time, time.Time) []CollectResult) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req collectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "요청 본문 JSON 파싱 실패")
			return
		}

		start, err := parseDate(req.Date)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "date는 YYYY-MM-DD 형식이어야 합니다")
			return
		}
		end := start.Add(24 * time.Hour)

		job := jobs.create(req.Date, dataset.Range{
			Start: start.Format(time.RFC3339),
			End:   end.Format(time.RFC3339),
		})

		go func() {
			results := collect(storage, start, end)
			jobs.complete(job.JobID, results)
			log.Printf("수집 완료 (jobId=%s, date=%s)", job.JobID, req.Date)
		}()

		log.Printf("수집 요청 접수 (jobId=%s, date=%s)", job.JobID, req.Date)
		writeJSON(w, http.StatusAccepted, job)
	}
}

// collectStatusHandler는 GET /v1/collect/{jobId}를 처리합니다 — collectHandler가
// 접수 즉시 돌려준 job의 지금 상태를 조회합니다. COMPLETED가 되면 완료 전의
// POST /v1/collect가 동기 응답으로 주던 것과 같은 results가 같이 옵니다.
func collectStatusHandler(jobs *collectJobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := r.PathValue("jobId")
		job, ok := jobs.get(jobID)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "해당 수집 작업을 찾을 수 없습니다")
			return
		}
		writeJSON(w, http.StatusOK, job)
	}
}

// marketManifestEntry는 한 마켓의 batch/stream 파일을 받아올 수 있는 URL입니다.
type marketManifestEntry struct {
	Market    string `json:"market"`
	BatchURL  string `json:"batchUrl"`
	StreamURL string `json:"streamUrl"`
}

// manifestResponse는 GET /v1/markets/data의 응답 본문입니다.
type manifestResponse struct {
	Date    string                `json:"date"`
	Markets []marketManifestEntry `json:"markets"`
}

// manifestHandler는 요청받은 날짜에 대해 upbit.TargetMarkets 전체의
// batch/stream 파일 URL 목록(매니페스트)을 돌려줍니다. 파일 내용 자체는
// GET /v1/markets/{market}/{batch|stream}에서 다루므로 여기서는 저장소를
// 건드리지 않고 URL만 만들어 반환합니다.
func manifestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		date := r.URL.Query().Get("date")
		if _, err := parseDate(date); err != nil {
			writeJSONError(w, http.StatusBadRequest, "date는 YYYY-MM-DD 형식이어야 합니다")
			return
		}

		markets := make([]marketManifestEntry, 0, len(upbit.TargetMarkets))
		for _, market := range upbit.TargetMarkets {
			markets = append(markets, marketManifestEntry{
				Market:    market,
				BatchURL:  fmt.Sprintf("/v1/markets/%s/batch?date=%s", market, date),
				StreamURL: fmt.Sprintf("/v1/markets/%s/stream?date=%s", market, date),
			})
		}

		log.Printf("매니페스트 요청 처리 완료 (date=%s, %d개 마켓)", date, len(markets))
		writeJSON(w, http.StatusOK, manifestResponse{Date: date, Markets: markets})
	}
}

// fileHandler는 저장된 market의 batch/stream 파일 내용을 그대로 서빙합니다.
// 파일이 아직 없으면 해당 마켓만 온디맨드로 수집한 뒤 서빙합니다(하이브리드 생성 설계).
func fileHandler(storage dataset.Storage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		market := r.PathValue("market")
		kind := r.PathValue("kind")

		if kind != "batch" && kind != "stream" {
			writeJSONError(w, http.StatusBadRequest, "kind는 batch 또는 stream이어야 합니다")
			return
		}
		if !isTargetMarket(market) {
			writeJSONError(w, http.StatusNotFound, "지원하지 않는 마켓입니다")
			return
		}

		start, err := parseDate(r.URL.Query().Get("date"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "date는 YYYY-MM-DD 형식이어야 합니다")
			return
		}
		end := start.Add(24 * time.Hour)

		log.Printf("[%s] %s 요청 수신 (date=%s)", market, kind, r.URL.Query().Get("date"))

		file, err := loadFile(storage, market, kind, start, end)
		if errors.Is(err, dataset.ErrNotFound) {
			log.Printf("[%s] %s 캐시 없음 — 온디맨드 수집 시작", market, kind)
			if err := ensureMarketCollected(storage, market, start, end); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "데이터 수집 실패: "+err.Error())
				return
			}
			file, err = loadFile(storage, market, kind, start, end)
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "파일 읽기 실패: "+err.Error())
			return
		}

		log.Printf("[%s] %s 응답 전송 완료", market, kind)
		writeJSON(w, http.StatusOK, file)
	}
}

func loadFile(storage dataset.Storage, market, kind string, start, end time.Time) (any, error) {
	if kind == "batch" {
		return storage.LoadBatch(market, start, end)
	}
	return storage.LoadStream(market, start, end)
}

func isTargetMarket(market string) bool {
	return slices.Contains(upbit.TargetMarkets, market)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
