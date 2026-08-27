package main

import (
	"fmt"
	"log"
	"time"

	"golang.org/x/sync/singleflight"

	"backend/dataset"
	"backend/upbit"
)

// onDemandCollect는 market+date 단위로 collectMarket 중복 실행을 막습니다.
// 같은 마켓의 batch/stream이 거의 동시에 요청되면(둘 다 파일이 없는 경우) 한 번만 수집하고
// 결과를 공유합니다 — collectMarket이 batch/stream을 항상 함께 만들기 때문입니다.
var onDemandCollect singleflight.Group

// ensureMarketCollected는 market+[start, end) 데이터가 storage에 없을 때만 온디맨드로 수집합니다.
func ensureMarketCollected(storage dataset.Storage, market string, start, end time.Time) error {
	key := market + "|" + start.Format(time.RFC3339)
	_, err, _ := onDemandCollect.Do(key, func() (any, error) {
		// forceFresh=false — 이 경로는 "없으면 채워라"는 온디맨드 보충용이라
		// 기존 캐시 스킵 최적화를 그대로 유지한다(collectMarket 주석 참고).
		_, _, err := collectMarket(storage, market, start, end, false)
		return nil, err
	})
	return err
}

// CollectResult는 마켓 하나에 대한 수집 결과입니다.
type CollectResult struct {
	Market     string `json:"market"`
	Status     string `json:"status"` // "ok" 또는 "error"
	BatchPath  string `json:"batchPath,omitempty"`
	StreamPath string `json:"streamPath,omitempty"`
	Error      string `json:"error,omitempty"`
}

// collectAllMarkets는 upbit.TargetMarkets 전체에 대해 [start, end) 구간 데이터를 수집합니다.
// 한 마켓에서 에러가 나도 나머지 마켓 수집은 계속 진행하고, 결과에 에러 상태로 기록합니다.
// onProgress는 마켓 하나가 끝날 때마다(성공/실패 무관) 호출됩니다 — 프론트
// 진행률 표시용(2026-08-12 추가, collectHandler가 collectJobStore.progress를
// 넘겨줌). nil이면 호출하지 않습니다 — 테스트 등 진행률이 필요 없는 호출부를
// 위한 것입니다.
func collectAllMarkets(storage dataset.Storage, start, end time.Time, onProgress func()) []CollectResult {
	results := make([]CollectResult, 0, len(upbit.TargetMarkets))

	for _, market := range upbit.TargetMarkets {
		// forceFresh=true — 이 함수는 명시적 "시세 수집 요청" 버튼 전용이라
		// (collectHandler가 주입하는 유일한 실사용 경로), 재요청 시 예전 캐시가
		// 아니라 항상 방금 받아온 데이터를 반영해야 한다(2026-08-26 요청).
		batchPath, streamPath, err := collectMarket(storage, market, start, end, true)
		if err != nil {
			log.Printf("[%s] 수집 실패: %v", market, err)
			results = append(results, CollectResult{
				Market: market,
				Status: "error",
				Error:  err.Error(),
			})
		} else {
			results = append(results, CollectResult{
				Market:     market,
				Status:     "ok",
				BatchPath:  batchPath,
				StreamPath: streamPath,
			})
		}

		if onProgress != nil {
			onProgress()
		}
	}

	return results
}

// collectMarket은 한 마켓에 대해 업비트 데이터를 전부 수집하여
// batch/stream JSON 파일로 저장합니다.
//
// forceFresh=false면, batch/stream이 이미 둘 다 저장돼 있을 때(같은
// market+기간을 재요청한 경우) 업비트를 아예 호출하지 않고 기존 경로를
// 그대로 돌려줍니다 — 2026-08-19 추가: 이전엔 이 확인이 storage.SaveBatch/
// SaveStream 저장 직전(putIfAbsent)에만 있어서, 이미 수집된 기간을 다시
// 요청해도 업비트 API는 항상 다시 호출되고(rate limit 소모, 실측 193초)
// 마지막 업로드 단계만 건너뛰는 문제가 있었습니다. ensureMarketCollected
// (온디맨드 보충 경로)가 이 모드를 씁니다.
//
// forceFresh=true면 위 스킵을 건너뛰고 항상 업비트를 다시 불러 결과로
// 기존 파일을 덮어씁니다(SaveBatch/SaveStream에 overwrite=true로 전달) —
// 2026-08-26 추가: "시세 수집 요청" 버튼을 다시 눌러도 예전 캐시가 그대로
// 나온다는 지적 대응. collectAllMarkets(명시적 수집 요청 전용)가 이 모드를
// 씁니다.
func collectMarket(storage dataset.Storage, market string, start, end time.Time, forceFresh bool) (batchPath, streamPath string, err error) {
	if !forceFresh {
		existing, err := storage.Exists(market, start, end)
		if err != nil {
			return "", "", fmt.Errorf("기존 데이터 확인 실패: %w", err)
		}
		if existing.AllExist() {
			fmt.Printf("[%s] 이미 수집된 데이터가 있어 건너뜀 -> %s, %s\n", market, existing.BatchPath, existing.StreamPath)
			return existing.BatchPath, existing.StreamPath, nil
		}
	}

	fmt.Printf("=== %s 수집 시작 ===\n", market)

	ticks, err := upbit.FetchTradeTicksForDate(market, start)
	if err != nil {
		return "", "", fmt.Errorf("체결 내역 조회 실패: %w", err)
	}

	seconds, err := upbit.FetchCandlesInRange("seconds", market, start, end)
	if err != nil {
		return "", "", fmt.Errorf("초봉 조회 실패: %w", err)
	}

	minutes, err := upbit.FetchCandlesInRange("minutes/1", market, start, end)
	if err != nil {
		return "", "", fmt.Errorf("분봉 조회 실패: %w", err)
	}

	days, err := upbit.FetchRecentCandles("days", market, end, 1)
	if err != nil {
		return "", "", fmt.Errorf("일봉 조회 실패: %w", err)
	}

	weeks, err := upbit.FetchRecentCandles("weeks", market, end, 1)
	if err != nil {
		return "", "", fmt.Errorf("주봉 조회 실패: %w", err)
	}

	months, err := upbit.FetchRecentCandles("months", market, end, 1)
	if err != nil {
		return "", "", fmt.Errorf("월봉 조회 실패: %w", err)
	}

	years, err := upbit.FetchRecentCandles("years", market, end, 1)
	if err != nil {
		return "", "", fmt.Errorf("연봉 조회 실패: %w", err)
	}

	batch := dataset.BuildBatch(market, start, end, days, weeks, months, years)
	batchPath, err = storage.SaveBatch(batch, start, end, forceFresh)
	if err != nil {
		return "", "", fmt.Errorf("batch 저장 실패: %w", err)
	}

	stream := dataset.BuildStream(market, start, end, seconds, minutes, ticks)
	streamPath, err = storage.SaveStream(stream, start, end, forceFresh)
	if err != nil {
		return "", "", fmt.Errorf("stream 저장 실패: %w", err)
	}

	fmt.Printf("[%s] 저장 완료 -> %s, %s\n\n", market, batchPath, streamPath)
	return batchPath, streamPath, nil
}
