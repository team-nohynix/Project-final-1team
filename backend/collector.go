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
		_, _, err := collectMarket(storage, market, start, end)
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
func collectAllMarkets(storage dataset.Storage, start, end time.Time) []CollectResult {
	results := make([]CollectResult, 0, len(upbit.TargetMarkets))

	for _, market := range upbit.TargetMarkets {
		batchPath, streamPath, err := collectMarket(storage, market, start, end)
		if err != nil {
			log.Printf("[%s] 수집 실패: %v", market, err)
			results = append(results, CollectResult{
				Market: market,
				Status: "error",
				Error:  err.Error(),
			})
			continue
		}

		results = append(results, CollectResult{
			Market:     market,
			Status:     "ok",
			BatchPath:  batchPath,
			StreamPath: streamPath,
		})
	}

	return results
}

// collectMarket은 한 마켓에 대해 업비트 데이터를 전부 수집하여
// batch/stream JSON 파일로 저장합니다.
func collectMarket(storage dataset.Storage, market string, start, end time.Time) (batchPath, streamPath string, err error) {
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
	batchPath, err = storage.SaveBatch(batch, start, end)
	if err != nil {
		return "", "", fmt.Errorf("batch 저장 실패: %w", err)
	}

	stream := dataset.BuildStream(market, start, end, seconds, minutes, ticks)
	streamPath, err = storage.SaveStream(stream, start, end)
	if err != nil {
		return "", "", fmt.Errorf("stream 저장 실패: %w", err)
	}

	fmt.Printf("[%s] 저장 완료 -> %s, %s\n\n", market, batchPath, streamPath)
	return batchPath, streamPath, nil
}
