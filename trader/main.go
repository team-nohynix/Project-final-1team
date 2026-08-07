package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sync"
	"time"

	"trader/bot"
	"trader/client"
	"trader/order"
	"trader/orderstore"
	"trader/replay"
	"trader/session"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	date := flag.String("date", "", "재생할 날짜 (YYYY-MM-DD, 필수)")
	speed := flag.Float64("speed", 60, "재생 배속 (이벤트 간 대기 시간을 이 값으로 나눔)")
	orderBucket := flag.String("order-bucket", "", "주문 기록을 저장할 S3 버킷 (비어있으면 ./orders 로컬 디렉터리에 저장)")
	flag.Parse()

	cfg := LoadConfig()

	if *date == "" {
		return fmt.Errorf("-date는 필수입니다 (YYYY-MM-DD)")
	}
	start, err := time.Parse("2006-01-02", *date)
	if err != nil {
		return fmt.Errorf("-date 형식이 올바르지 않습니다: %w", err)
	}
	start = start.UTC()
	end := start.Add(24 * time.Hour)

	httpClient := client.NewHTTPClient()

	// 팀 결정(2026-08-06): 두 개 이상의 트레이더, 또는 트레이더와 리플레이 엔진이
	// 동시에 실행되는 상황은 undefined이므로 애초에 막는다 — 같은 매칭 엔진
	// 호가창에 서로 다른 실행의 주문이 섞여 들어가는 걸 방지한다. orderapi의 세션
	// API로 실행 시작 시점에 딱 한 번만 배타적으로 클레임한다(주문 하나하나가
	// 오가는 경로에는 관여하지 않으므로 NFR-01 처리량에 영향 없음). run()을 별도
	// 함수로 뽑아낸 이유도 이것과 직접 관련 있다 — log.Fatal은 os.Exit로 즉시
	// 종료해 defer(세션 반납)를 건너뛰므로, 세션 반납이 항상 실행되도록 에러를
	// 반환하는 형태로 바꾸고 main()에서 마지막에 한 번만 log.Fatal한다.
	sessionClient := session.Client{HTTPClient: httpClient, BaseURL: cfg.OrderAPIURL}
	sessionID, ttlSeconds, err := sessionClient.Claim(context.Background(), "trader")
	if err != nil {
		return fmt.Errorf("세션 클레임 실패 — 트레이더/시뮬레이터는 동시에 하나만 실행할 수 있습니다: %w", err)
	}
	log.Printf("세션 클레임 완료 (sessionId=%s)", sessionID)

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

	manifest, err := client.FetchManifest(context.Background(), httpClient, cfg.BackendURL, *date)
	if err != nil {
		return fmt.Errorf("매니페스트 조회 실패: %w", err)
	}
	log.Printf("매니페스트 수신: %d개 마켓", len(manifest.Markets))

	// 주문 접수 API(orderapi)에 실제로 POST /v1/orders를 보냅니다.
	// RecordingSubmitter로 감싸서, 성공적으로 "제출"된(202 응답을 받은) 주문만 recorder에 남깁니다(FR-17).
	recorder := order.NewInMemoryRecorder()
	var submitter order.OrderSubmitter = order.RecordingSubmitter{
		Next:     order.HTTPOrderSubmitter{Client: httpClient, BaseURL: cfg.OrderAPIURL},
		Recorder: recorder,
	}

	// 주문 기록용 S3 버킷은 아직 없어서(인프라팀에 요청 중), 기본은 로컬 파일로 저장합니다.
	// 버킷이 생기면 -order-bucket=team1-truss-order-records 처럼 이름만 넘기면 됩니다.
	var orderStorage orderstore.Storage
	if *orderBucket != "" {
		orderStorage = orderstore.NewS3Storage(*orderBucket)
	} else {
		orderStorage = orderstore.NewLocalFileStorage("orders")
	}

	// 마켓별 상태를 미리 만들어둡니다 — 마켓별 알고리즘 봇(ReplayMarket 안)과
	// 전체 조망형 AI 봇(RunGlobalBots)이 같은 MarketState를 공유해서 봅니다.
	states := make(map[string]*bot.MarketState, len(manifest.Markets))
	for _, entry := range manifest.Markets {
		states[entry.Market] = bot.NewMarketState(bot.PriceHistorySize)
	}

	// 전체 마켓 재생이 다 끝나면(아래 wg.Wait()) 전체 조망형 봇도 같이 멈춥니다.
	ctx, cancel := context.WithCancel(context.Background())

	var globalWG sync.WaitGroup
	globalWG.Go(func() {
		replay.RunGlobalBots(ctx, states, *speed, submitter)
	})

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string

	// 마켓당 고루틴 1개, 전부 NewHTTPClient가 만든 단일 클라이언트를 공유합니다.
	// 한 마켓의 실패가 다른 마켓 재생을 막지 않도록 에러는 로그로만 남기고 계속 진행합니다.
	for _, entry := range manifest.Markets {
		wg.Go(func() {
			if err := replay.ReplayMarket(ctx, httpClient, cfg.BackendURL, entry, *speed, submitter, states[entry.Market]); err != nil {
				log.Printf("[%s] 재생 실패: %v", entry.Market, err)
				mu.Lock()
				failed = append(failed, entry.Market)
				mu.Unlock()
			}
		})
	}

	wg.Wait()
	cancel()
	globalWG.Wait()

	// 마켓 재생 + 전체 조망형 봇이 전부 끝난 뒤 한 번에 기록을 저장합니다 — 전체 조망형
	// 봇은 마켓 재생 순서와 무관하게 끝까지 돌기 때문에, 마켓 하나가 끝나자마자 저장하면
	// 그 이후 전체 조망형 봇이 그 마켓에 낸 주문을 놓칠 수 있습니다.
	for _, entry := range manifest.Markets {
		orders := recorder.Snapshot(entry.Market)
		if len(orders) == 0 {
			continue
		}
		path, err := orderStorage.Save(entry.Market, start, end, orders)
		if err != nil {
			log.Printf("[%s] 주문 기록 저장 실패: %v", entry.Market, err)
			continue
		}
		log.Printf("[%s] 주문 기록 저장 완료 (%d건) -> %s", entry.Market, len(orders), path)
	}

	if len(failed) > 0 {
		log.Printf("전체 재생 완료 — 실패한 마켓(%d개): %v", len(failed), failed)
		return nil
	}
	log.Printf("전체 재생 완료 — %d개 마켓 전부 성공", len(manifest.Markets))
	return nil
}
