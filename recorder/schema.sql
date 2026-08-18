-- docs/erd.md 기준 스키마 (MySQL 8+). replay_session_id/REPLAY_SESSION은 만들지
-- 않기로 결정(팀 결정, 2026-08-07 — erd.md §4)해 제외했다. 마이그레이션 툴 없이
-- mysql 클라이언트로 손으로 적용한다(이 repo의 dev-simple 컨벤션 — docker-compose들도
-- 볼륨/영속성 없이 극단적으로 단순함).
--
-- 로컬: mysql -h127.0.0.1 -P3306 -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" "$MYSQL_DATABASE" < schema.sql
--
-- id/코드 컬럼은 TEXT가 아니라 VARCHAR(N)입니다 — MySQL은 TEXT/BLOB 컬럼을 그대로
-- 기본키·인덱스로 못 씁니다(길이 지정이 필요). price/quantity는 DECIMAL(24,8) —
-- MySQL의 DECIMAL은 길이를 안 주면 DECIMAL(10,0)(소수부 없음)으로 잘려서, 8자리
-- 소수(matching/orderbook.normalizePrice의 Truncate(8)과 동일한 정밀도)를 명시했다.
-- TIMESTAMPTZ 대신 DATETIME을 쓴다 — 이 프로젝트는 시각을 항상 UTC 문자열로
-- 명시적으로 다루므로(orderapi의 nowISO() 등), MySQL TIMESTAMP의 세션 타임존 기반
-- 자동 변환이나 2038년 한계가 오히려 불필요한 변수라 DATETIME이 더 안전하다.

CREATE TABLE IF NOT EXISTS market (
    market_code   VARCHAR(20) PRIMARY KEY,
    korean_name   VARCHAR(50) NOT NULL,
    symbol        VARCHAR(20) NOT NULL
);

CREATE TABLE IF NOT EXISTS trade_order (
    order_id            VARCHAR(64) PRIMARY KEY,
    client_request_id   VARCHAR(128) UNIQUE,
    market_code         VARCHAR(20) NOT NULL,
    side                VARCHAR(10) NOT NULL CHECK (side IN ('BUY','SELL')),
    price               DECIMAL(24,8) NOT NULL,
    quantity            DECIMAL(24,8) NOT NULL,
    remaining_quantity  DECIMAL(24,8) NOT NULL,
    status              VARCHAR(20) NOT NULL CHECK (status IN ('ACCEPTED','PARTIALLY_FILLED','FILLED','CANCELED')),
    mode                VARCHAR(20) NOT NULL CHECK (mode IN ('PAPER_TRADING','REPLAY')),
    submitted_at        DATETIME(3) NOT NULL,
    canceled_at         DATETIME(3) NULL,
    created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    -- source_order_id는 리플레이 주문만 값이 있는 자기참조 컬럼입니다(FR-18 검증
    -- "동일 파일 재생 시 총 주문 수 일치"용, docs/erd.md 참고). 의도적으로 FK가
    -- 아닙니다 — execution.buy_order_id/sell_order_id와 같은 이유로, 원본 페이퍼
    -- 트레이딩 주문의 NEW 이벤트를 기록기가 아직 처리하기 전에 그 리플레이 주문의
    -- NEW 이벤트가 먼저 도착할 수 있습니다(orders 토픽 하나를 순서대로 읽지만,
    -- 원본 주문과 리플레이 주문은 서로 다른 실행에서 몇 시간~며칠 간격을 두고
    -- 발생할 수 있어 FK로 강제하면 리플레이 실행 시점에 원본이 DB에서 지워졌거나
    -- 아직 없을 때 INSERT IGNORE가 이 행 전체를 조용히 건너뜁니다.
    source_order_id     VARCHAR(64) NULL,
    CONSTRAINT fk_trade_order_market FOREIGN KEY (market_code) REFERENCES market(market_code)
);
CREATE INDEX idx_trade_order_market_status ON trade_order (market_code, status);
CREATE INDEX idx_trade_order_source_order ON trade_order (source_order_id);

CREATE TABLE IF NOT EXISTS execution (
    execution_id   VARCHAR(64) PRIMARY KEY,
    market_code    VARCHAR(20) NOT NULL,
    -- buy_order_id/sell_order_id는 의도적으로 trade_order에 대한 FK가 아닙니다 —
    -- 기록기는 orders/executions를 서로 독립된 리더로 소비하므로, 아직 NEW를
    -- 못 본 주문의 체결이 먼저 도착할 수 있습니다(mode 컬럼과 같은 이유). FR-09
    -- 검증 기준("Kafka 발행 건수와 DB 저장 건수가 일치")이 execution 저장 자체를
    -- 그 경우에도 절대 막지 말라고 요구하므로, FK로 강제하면 안 됩니다.
    buy_order_id   VARCHAR(64) NOT NULL,
    sell_order_id  VARCHAR(64) NOT NULL,
    price          DECIMAL(24,8) NOT NULL,
    quantity       DECIMAL(24,8) NOT NULL,
    -- mode는 NULL을 허용합니다 — orders/executions 리더가 서로 독립적으로 각자
    -- 뒤처진 만큼 따라잡기 때문에, 기록기가 스트림을 처음부터(또는 재시작 후
    -- 밀린 채로) 따라잡는 동안 아직 두 주문 다 못 본 체결이 실제로 생길 수
    -- 있습니다(recorder/store/apply.go의 ResolveMode가 이 경우 ""를 반환) —
    -- execution 행 자체는 항상 저장해야 하므로, "모른다"를 유효한 상태로
    -- 표현해야 합니다.
    mode           VARCHAR(20) NULL CHECK (mode IN ('PAPER_TRADING','REPLAY')),
    executed_at    DATETIME(3) NOT NULL,
    CONSTRAINT fk_execution_market FOREIGN KEY (market_code) REFERENCES market(market_code)
);
-- FR-13 "최근 순으로 거래 내역을 조회" 페이지네이션용.
CREATE INDEX idx_execution_market_executed_at ON execution (market_code, executed_at DESC);
CREATE INDEX idx_execution_buy_order  ON execution (buy_order_id);
CREATE INDEX idx_execution_sell_order ON execution (sell_order_id);

-- FR-11 마켓 재분배 이력. matching이 assignments 토픽에 배정/반납 이벤트를
-- 발행하고(matching/kafkaclient/assignment_producer.go), 기록기가 그걸 구독해
-- 채웁니다. released_at IS NULL이면 지금 담당 중이라는 뜻 — "한 마켓은 항상
-- 정확히 한 엔진만 담당한다"(1.2.2)를 이 조건으로 검증할 수 있습니다. 리플레이
-- 분산(FR-19)의 REPLAY_ENGINE_MARKET과 달리 이 배정은 측정된 부하로 동적으로
-- 정해지므로 사후 재계산이 불가능해 기록할 가치가 있다(docs/erd.md §4 참고).
CREATE TABLE IF NOT EXISTS matching_engine_assignment (
    assignment_id       VARCHAR(64) PRIMARY KEY,
    market_code         VARCHAR(20) NOT NULL,
    engine_instance_id  VARCHAR(64) NOT NULL,
    assigned_at         DATETIME(3) NOT NULL,
    released_at         DATETIME(3) NULL,
    CONSTRAINT fk_assignment_market FOREIGN KEY (market_code) REFERENCES market(market_code)
);
CREATE INDEX idx_assignment_market_open ON matching_engine_assignment (market_code, released_at);

-- 20개 마켓 마스터 데이터 시드 (backend/upbit.TargetMarkets, orderapi/validate.TargetMarkets와
-- 같은 20개 목록 — 모듈 독립 원칙에 따라 이 파일에도 값을 그대로 적어둔다).
INSERT INTO market (market_code, korean_name, symbol) VALUES
    ('KRW-USDT', '테더', 'USDT'),
    ('KRW-BTC',  '비트코인', 'BTC'),
    ('KRW-XRP',  '리플', 'XRP'),
    ('KRW-ETH',  '이더리움', 'ETH'),
    ('KRW-ONDO', '온도파이낸스', 'ONDO'),
    ('KRW-LA',   '라이덱스', 'LA'),
    ('KRW-SHIB', '시바이누', 'SHIB'),
    ('KRW-RE',   '리버스', 'RE'),
    ('KRW-DOGE', '도지코인', 'DOGE'),
    ('KRW-SLX',  '솔렛수', 'SLX'),
    ('KRW-KAITO','카이토', 'KAITO'),
    ('KRW-SOL',  '솔라나', 'SOL'),
    ('KRW-XLM',  '스텔라루멘', 'XLM'),
    ('KRW-WLD',  '월드코인', 'WLD'),
    ('KRW-MIRA', '미라', 'MIRA'),
    ('KRW-ERA',  '에라', 'ERA'),
    ('KRW-ADA',  '에이다', 'ADA'),
    ('KRW-AI',   '에이아이', 'AI'),
    ('KRW-NEAR', '니어프로토콜', 'NEAR'),
    ('KRW-ARX',  '아크스', 'ARX')
ON DUPLICATE KEY UPDATE market_code = market_code;
