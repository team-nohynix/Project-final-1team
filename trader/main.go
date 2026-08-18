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

// run의 반환값 err는 named return입니다 — 아래 defer가 "run이 최종적으로
// 에러를 반환했는지"를 보고 orderapi에 실행 결과(완료/실패)를 보고합니다
// (2026-08-12, 프론트 "실행 결과" 화면 지원). resultMessage는 err가 nil이어도
// (마켓 일부만 실패해도 전체 실행 자체는 실패로 안 치는 기존 정책, 아래
// "실패한 마켓" 로그와 동일) 남길 만한 요약이 있으면 채웁니다.
func run() (err error) {
	var resultMessage string
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

		// "COMPLETED"/"FAILED"는 orderapi/session.RunStatusCompleted/Failed와
		// 같은 문자열입니다 — 모듈 간 타입 비공유 원칙에 따라 문자열 그대로
		// 다시 씁니다(orderapi가 실제로 검사하는 값은 releaseRequest.Status
		// 필드뿐이라 상수 공유가 필요 없습니다).
		status := "COMPLETED"
		message := resultMessage
		if err != nil {
			status = "FAILED"
			message = err.Error()
		}
		if relErr := sessionClient.Release(context.Background(), sessionID, status, message); relErr != nil {
			log.Printf("세션 반납 실패 (sessionId=%s): %v", sessionID, relErr)
		} else {
			log.Printf("세션 반납 완료 (sessionId=%s, status=%s)", sessionID, status)
		}
	}()

	manifest, err := client.FetchManifest(context.Background(), httpClient, cfg.BackendURL, *date)
	if err != nil {
		return fmt.Errorf("매니페스트 조회 실패: %w", err)
	}
	log.Printf("매니페스트 수신: %d개 마켓", len(manifest.Markets))

	// 주문 접수 API(orderapi)에 실제로 POST /v1/orders를 보냅니다.
	// RetryingSubmitter가 429(RDS 백프레셔)만 지수 백오프로 재시도하고, 그
	// 최종 결과를 RecordingSubmitter가 성공했을 때만 recorder에 남깁니다(FR-17,
	// "기록 건수와 접수 건수 일치"). 데코레이터 순서가 중요합니다 — Recording이
	// 바깥이라야 재시도 도중의 중간 실패가 아니라 재시도까지 다 끝난 최종
	// 결과만 기록 여부 판단에 씁니다.
	recorder := order.NewInMemoryRecorder()
	var submitter order.OrderSubmitter = order.RecordingSubmitter{
		Next: order.RetryingSubmitter{
			Next: order.HTTPOrderSubmitter{Client: httpClient, BaseURL: cfg.OrderAPIURL},
		},
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

	// AI 트레이더(전체 조망형 봇)가 쓸 Bedrock 클라이언트입니다. 자격증명/모델
	// 액세스가 아직 준비 안 된 로컬 환경이라도 여기서 바로 실패하진 않습니다 —
	// SDK 설정 로드 자체는 대체로 성공하고, 실제 호출이 실패해야 그때 드러납니다
	// (bot.MomentumAIBot.Decide가 그 실패를 로그만 남기고 넘어가게 처리해둠).
	bedrockClient, err := bot.NewBedrockClient(context.Background(), cfg.BedrockRegion, cfg.BedrockModelID)
	if err != nil {
		return fmt.Errorf("Bedrock 클라이언트 생성 실패: %w", err)
	}

	// 전체 마켓 재생이 다 끝나면(아래 wg.Wait()) 전체 조망형 봇도 같이 멈춥니다.
	ctx, cancel := context.WithCancel(context.Background())

	var globalWG sync.WaitGroup
	globalWG.Go(func() {
		replay.RunGlobalBots(ctx, states, submitter, bedrockClient)
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
		resultMessage = fmt.Sprintf("실패한 마켓 %d개: %v", len(failed), failed)
		log.Printf("전체 재생 완료 — %s", resultMessage)
		return nil
	}
	log.Printf("전체 재생 완료 — %d개 마켓 전부 성공", len(manifest.Markets))
	return nil
}
