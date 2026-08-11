<script setup>
import { ref } from 'vue'

// 20 KRW markets dummy
const markets = Array.from({ length: 20 }).map(
  (_, i) => `MK${(i + 1).toString().padStart(2, '0')}/KRW`,
)

// distribute markets across 4 engines without overlap
const engines = ref([
  { id: 'engine-01', color: '#3478f6', status: 'running', markets: markets.slice(0, 5) },
  { id: 'engine-02', color: '#2ed39a', status: 'running', markets: markets.slice(5, 10) },
  { id: 'engine-03', color: '#ffb84d', status: 'running', markets: markets.slice(10, 15) },
  { id: 'engine-04', color: '#9b7bff', status: 'running', markets: markets.slice(15, 20) },
])

const distributionOk = ref(true) // '중복 없음' badge

// order matching flow (price/time priority) - dummy steps
const matchingSteps = ref([
  { step: 1, title: '매도 주문', price: '99,500 KRW', qty: '0.5' },
  { step: 2, title: '매수 주문', price: '99,500 KRW', qty: '0.5' },
  { step: 3, title: '체결', price: '99,500 KRW', qty: '0.5' },
  { step: 4, title: '남은 수량 호가창 유지', price: '-', qty: '0.0' },
])

const orderbookRecovery = ref({
  replayed: 10000,
  total: 10000,
  missing: 0,
  timeSec: 38.2,
  goalSec: 60,
})

const executions = ref([
  {
    execId: 'EX-9001',
    maker: 'ORD-1001',
    taker: 'ORD-1002',
    kafka: { partition: 2, offset: 124 },
    pgSaved: true,
    savedAt: '2026-07-30 12:34:22',
  },
  {
    execId: 'EX-9002',
    maker: 'ORD-1010',
    taker: 'ORD-1011',
    kafka: { partition: 1, offset: 857 },
    pgSaved: true,
    savedAt: '2026-07-30 12:35:01',
  },
])
</script>

<template>
  <div class="matching-page">
    <header class="page-header">
      <div>
        <h2>매칭 엔진</h2>
        <p>가격·시간 우선 원칙에 따른 주문 처리와 마켓 분배 현황</p>
      </div>
    </header>

    <section class="top-card panel">
      <div class="top-card-left">
        <h3>마켓별 엔진 분배</h3>
        <div class="market-summary">
          20개 KRW 마켓이 여러 매칭 엔진에 중복 없이 분배되어 있습니다.
        </div>
        <div class="market-list">
          <div v-for="m in markets" :key="m" class="market-item">{{ m }}</div>
        </div>
      </div>
      <div class="top-card-right">
        <div class="badge" :class="{ ok: distributionOk }">
          {{ distributionOk ? '중복 없음' : '중복 존재' }}
        </div>
      </div>
    </section>

    <section class="engine-grid">
      <div v-for="e in engines" :key="e.id" class="engine-card panel">
        <div class="engine-title">
          <span class="engine-dot" :style="{ backgroundColor: e.color }"></span>
          <strong>{{ e.id }}</strong>
          <span class="engine-status">{{ e.status === 'running' ? '실행 중' : '중지' }}</span>
        </div>
        <div class="engine-markets">
          <div v-for="m in e.markets" :key="m" class="engine-market">{{ m }}</div>
        </div>
      </div>
    </section>

    <section class="middle-row">
      <article class="panel match-flow">
        <h4>주문 매칭 순서</h4>
        <ol class="steps">
          <li v-for="s in matchingSteps" :key="s.step">
            <div class="step-title">{{ s.step }}. {{ s.title }}</div>
            <div class="step-detail">가격: {{ s.price }} · 수량: {{ s.qty }}</div>
          </li>
        </ol>
      </article>

      <article class="panel recovery-card">
        <h4>호가창 복구</h4>
        <div class="recovery-row">
          <span>재생 완료</span
          ><strong>{{ orderbookRecovery.replayed }} / {{ orderbookRecovery.total }}</strong>
        </div>
        <div class="recovery-row">
          <span>이벤트 누락</span><strong>{{ orderbookRecovery.missing }}건</strong>
        </div>
        <div class="recovery-row">
          <span>복구 시간</span><strong>{{ orderbookRecovery.timeSec }}초</strong>
        </div>
        <div class="recovery-goal">목표: {{ orderbookRecovery.goalSec }}초 이하</div>
        <div class="recovery-status ok">정상</div>
      </article>
    </section>

    <section class="panel executions">
      <h4>체결 결과</h4>
      <div class="exec-list">
        <div v-for="ex in executions" :key="ex.execId" class="exec-row">
          <div class="exec-left">
            <div class="exec-id">Execution ID: {{ ex.execId }}</div>
            <div class="exec-orders">Maker: {{ ex.maker }} / Taker: {{ ex.taker }}</div>
          </div>
          <div class="exec-right">
            <div>Kafka P {{ ex.kafka.partition }} · Off {{ ex.kafka.offset }}</div>
            <div>
              Postgres: {{ ex.pgSaved ? '저장됨' : '미저장' }}
              <span v-if="ex.savedAt">· {{ ex.savedAt }}</span>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.matching-page .top-card {
  display: flex;
  justify-content: space-between;
  gap: 20px;
  align-items: flex-start;
}
.top-card-left {
  flex: 1 1 auto;
}
.top-card-right {
  width: 160px;
  display: flex;
  align-items: flex-start;
  justify-content: flex-end;
}
.badge {
  padding: 8px 12px;
  border-radius: 12px;
  background: #2b4b59;
  color: #9fb0c1;
  font-weight: 700;
}
.badge.ok {
  background: #08323a;
  color: #2ed39a;
}
.market-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}
.market-item {
  background: #071824;
  padding: 6px 8px;
  border-radius: 8px;
  border: 1px solid #173141;
  color: #9fb0c1;
  font-size: 12px;
}

.engine-grid {
  display: flex;
  gap: 12px;
  margin-top: 16px;
  flex-wrap: wrap;
}
.engine-card {
  flex: 1 1 calc(25% - 12px);
  min-width: 200px;
}
.engine-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.engine-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
}
.engine-markets {
  margin-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.engine-market {
  font-size: 13px;
  color: #9fb0c1;
}

.middle-row {
  display: flex;
  gap: 20px;
  margin-top: 16px;
}
.match-flow {
  flex: 1 1 58%;
}
.recovery-card {
  flex: 1 1 42%;
}
.steps {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.step-title {
  font-weight: 700;
}
.recovery-row {
  display: flex;
  justify-content: space-between;
  padding: 8px 0;
  color: #9fb0c1;
}
.recovery-goal {
  color: #9fb0c1;
  margin-top: 8px;
}
.recovery-status.ok {
  margin-top: 12px;
  color: #2ed39a;
  font-weight: 700;
}

.executions {
  margin-top: 16px;
}
.exec-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.exec-row {
  display: flex;
  justify-content: space-between;
  padding: 12px;
  background: #071824;
  border-radius: 8px;
  border: 1px solid #173141;
}
.exec-id {
  font-weight: 700;
}

@media (max-width: 1100px) {
  .top-card {
    flex-direction: column;
  }
  .engine-grid {
    flex-direction: column;
  }
  .middle-row {
    flex-direction: column;
  }
  .engine-card {
    width: 100%;
  }
}
</style>
