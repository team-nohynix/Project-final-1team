package main

import (
	"context"
	"database/sql"
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"recorder/archive"
	"recorder/events"
	rkafka "recorder/kafka"
	"recorder/store"
)

// 아카이브 마이크로배치는 건수 또는 시간 중 먼저 도달하면 플러시합니다 —
// matching/engine.Engine의 스냅샷 이중 트리거와 같은 패턴. orders/executions는
// 각자 별도 Batcher를 써서 한쪽이 몰려도 다른 쪽 플러시 주기가 밀리지 않습니다.
const (
	archiveFlushEvery    = 500
	archiveFlushInterval = 30 * time.Second

	// 컨슈머 그룹 ID는 고정 상수입니다 — matching과 마찬가지로 배포 전체가
	// 공유해야 하는 값이라 환경변수가 아닙니다. orders/executions/assignments를
	// 별도 그룹으로 나눠서 서로의 파티션 배정에 관여하지 않게 합니다.
	ordersGroupID      = "recorder-orders"
	executionsGroupID  = "recorder-executions"
	assignmentsGroupID = "recorder-assignments"
)

func main() {
	cfg := LoadConfig()
	ctx := context.Background()

	db, err := sql.Open("mysql", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("MySQL 드라이버 초기화 실패: %v", err)
	}
	defer db.Close()
	// sql.Open은 실제로 연결하지 않고 지연 연결하므로, 시작 시점에 곧바로 확인해서
	// (연결 문자열이 잘못됐거나 DB가 안 떠 있으면) 여기서 바로 실패가 드러나게 합니다.
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("MySQL 연결 확인 실패: %v", err)
	}
	dbStore := store.NewMySQLStore(db)

	var archiveStore archive.Store
	if cfg.ArchiveBucket != "" {
		archiveStore = archive.NewS3Store(cfg.ArchiveBucket)
	} else {
		archiveStore = archive.NewLocalStore("records")
	}
	orderBatcher := archive.NewBatcher(archiveStore, "orders", archiveFlushEvery)
	execBatcher := archive.NewBatcher(archiveStore, "executions", archiveFlushEvery)
	go runPeriodicFlush(ctx, orderBatcher, archiveFlushInterval)
	go runPeriodicFlush(ctx, execBatcher, archiveFlushInterval)

	orderReader := rkafka.NewOrderReader(cfg.KafkaBroker, cfg.OrdersTopic, ordersGroupID)
	defer orderReader.Close()
	execReader := rkafka.NewExecutionReader(cfg.KafkaBroker, cfg.ExecutionsTopic, executionsGroupID)
	defer execReader.Close()
	assignmentReader := rkafka.NewAssignmentReader(cfg.KafkaBroker, cfg.AssignmentsTopic, assignmentsGroupID)
	defer assignmentReader.Close()

	archiveDest := cfg.ArchiveBucket
	if archiveDest == "" {
		archiveDest = "(로컬 ./records)"
	}
	log.Printf("기록기 시작 (broker=%s, orders=%s, executions=%s, assignments=%s, archive=%s)",
		cfg.KafkaBroker, cfg.OrdersTopic, cfg.ExecutionsTopic, cfg.AssignmentsTopic, archiveDest)

	// execReader.Run/orderReader.Run은 이제 메시지를 배치로 모아 넘겨줍니다
	// (RDS 백프레셔 대응 배칭, 2026-08-07). S3 아카이브 배치(execBatcher/
	// orderBatcher)는 원래부터 인메모리 버퍼링이라 굳이 같이 안 묶어도 저렴하므로,
	// RDS 배치 하나당 건별로 계속 Add합니다 — RDS 왕복 배칭과 S3 아카이브
	// 배칭은 서로 다른 목적의, 독립적인 두 배치입니다(크기/주기도 다름).
	go func() {
		err := execReader.Run(ctx, func(ctx context.Context, evs []events.ExecutionEvent) error {
			for _, ev := range evs {
				execBatcher.Add(ev)
			}
			return store.ApplyExecutionEvents(ctx, dbStore, evs)
		})
		log.Fatalf("executions 리더 종료: %v", err)
	}()

	go func() {
		err := assignmentReader.Run(ctx, func(ctx context.Context, ev events.AssignmentEvent) error {
			return store.ApplyAssignmentEvent(ctx, dbStore, ev)
		})
		log.Fatalf("assignments 리더 종료: %v", err)
	}()

	err = orderReader.Run(ctx, func(ctx context.Context, evs []events.OrderEvent) error {
		for _, ev := range evs {
			orderBatcher.Add(ev)
		}
		return store.ApplyOrderEvents(ctx, dbStore, evs)
	})
	log.Fatalf("orders 리더 종료: %v", err)
}

func runPeriodicFlush(ctx context.Context, b *archive.Batcher, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Flush()
		}
	}
}
