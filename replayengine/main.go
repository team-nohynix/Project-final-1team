package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"sync"
	"time"

	"replayengine/orderstore"
	"replayengine/session"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	date := flag.String("date", "", "리플레이할 기록의 날짜 (YYYY-MM-DD, 필수 — trader가 그 -date로 기록한 세션)")
	speed := flag.Float64("speed", 60, "재생 배속 (이벤트 간 대기 시간을 이 값으로 나눔)")
	orderBucket := flag.String("order-bucket", "", "주문 기록을 읽어올 S3 버킷 (비어있으면 ./orders 로컬 디렉터리에서 읽음)")
	shardIndex := flag.Int("shard-index", 0, "분산 실행 시 이 인스턴스가 담당할 샤드 번호 (0부터 시작, FR-19)")
	shardCount := flag.Int("shard-count", 1, "분산 실행 시 전체 인스턴스 수 (기본 1 = 혼자 20개 마켓 전부 담당)")
	runID := flag.String("run-id", "", "같은 리플레이 실행에 속한 여러 샤드가 공유할 식별자 (FR-19 분산 실행 — 세션 가드를 그룹으로 함께 통과하려면 모든 샤드에 똑같은 값을 줘야 함; 비우면 이 샤드 혼자만의 단독 실행으로 취급)")
	fromTS := flag.Int64("from-ts", 0, "이 시각(Unix ms) 이전 주문은 재생 제외 (0이면 제한 없음, FR-27 구간 지정)")
	toTS := flag.Int64("to-ts", 0, "이 시각(Unix ms) 이후 주문은 재생 제외 (0이면 제한 없음, FR-27 구간 지정)")
	flag.Parse()

	if *date == "" {
		return fmt.Errorf("-date는 필수입니다 (YYYY-MM-DD, trader가 기록할 때 쓴 날짜와 같아야 함)")
	}
	if *shardCount < 1 || *shardIndex < 0 || *shardIndex >= *shardCount {
		return fmt.Errorf("-shard-index(%d)는 0 이상 -shard-count(%d) 미만이어야 합니다", *shardIndex, *shardCount)
	}

	// trader/main.go와 동일하게 UTC 캘린더 일 기준으로 해석합니다 — trader가 기록
	// 파일을 쓸 때 이 방식으로 start/end를 계산했으므로, 여기서도 똑같이 계산해야
	// 같은 objectKey(=같은 파일 경로)를 가리킵니다.
	start, err := time.Parse("2006-01-02", *date)
	if err != nil {
		return fmt.Errorf("-date 형식이 올바르지 않습니다: %w", err)
	}
	start = start.UTC()
	end := start.Add(24 * time.Hour)

	cfg := LoadConfig()

	var storage orderstore.Storage
	if *orderBucket != "" {
		storage = orderstore.NewS3Storage(*orderBucket)
	} else {
		storage = orderstore.NewLocalFileStorage("orders")
	}

	httpClient := NewHTTPClient()
	submitter := HTTPOrderSubmitter{Client: httpClient, BaseURL: cfg.OrderAPIURL}

	// 팀 결정(2026-08-06), trader/main.go와 같은 이유 — 두 개 이상의 트레이더/
	// 리플레이 엔진이 동시에 실행되는 상황을 실행 시작 시점에 막는다. run()을
	// 별도 함수로 뽑아낸 이유도 같다: log.Fatal이 defer(세션 반납)를 건너뛰지
	// 않도록, 에러를 반환해 main()에서 마지막에 한 번만 처리한다.
	sessionClient := session.Client{HTTPClient: httpClient, BaseURL: cfg.OrderAPIURL}
	sessionID, ttlSeconds, err := sessionClient.Claim(context.Background(), "replayengine", *runID)
	if err != nil {
		return fmt.Errorf("세션 클레임 실패 — 트레이더/시뮬레이터는 동시에 하나만 실행할 수 있고, 이미 다른 실행 그룹이 활성 상태입니다: %w", err)
	}
	log.Printf("세션 클레임 완료 (sessionId=%s, runId=%q)", sessionID, *runID)

	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	var heartbeatWG sync.WaitGroup
	heartbeatWG.Go(func() {
		session.RunHeartbeat(heartbeatCtx, sessionClient, sessionID, ttlSeconds)
	})
	defer func() {
		stopHeartbeat()
		heartbeatWG.Wait()
		if err := sessionClient.Release(context.Background(), sessionID); err != nil {
			log.Printf("세션 반납 실패 (sessionId=%s): %v", sessionID, err)
		} else {
			log.Printf("세션 반납 완료 (sessionId=%s)", sessionID)
		}
	}()

	ctx := context.Background()
	var wg sync.WaitGroup
	skipped, started := 0, 0

	for i, market := range TargetMarkets {
		if i%*shardCount != *shardIndex {
			continue // FR-19: 마켓 단위 분산 — 이 샤드가 담당할 마켓이 아님
		}

		orders, err := storage.Load(market, start, end)
		if errors.Is(err, orderstore.ErrNotFound) {
			skipped++
			continue // 그 기간 동안 이 마켓엔 기록된 주문이 없었음 — 정상
		}
		if err != nil {
			log.Printf("[%s] 주문 기록 로드 실패: %v", market, err)
			continue
		}

		orders = filterRange(orders, *fromTS, *toTS)
		if len(orders) == 0 {
			skipped++
			continue
		}

		started++
		wg.Go(func() {
			replayMarket(ctx, market, orders, *speed, submitter)
		})
	}

	log.Printf("리플레이 시작 — 담당 마켓 중 %d개 재생, %d개는 기록 없음/구간 필터로 건너뜀 (shard %d/%d, orderapi=%s)",
		started, skipped, *shardIndex, *shardCount, cfg.OrderAPIURL)

	wg.Wait()
	log.Print("전체 리플레이 완료")
	return nil
}
