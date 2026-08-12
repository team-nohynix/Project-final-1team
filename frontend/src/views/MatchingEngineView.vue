<script setup>
import { ref } from 'vue'

// Markets list will be fetched by matching-engine API; none exists yet — show placeholder
const markets = ref([])

// distribute markets across 4 engines without overlap
const engines = ref([])

// distribution status unknown until API exists
const distributionOk = ref(null) // null = unknown

// order matching flow (price/time priority) - dummy steps
const matchingSteps = ref([])

const orderbookRecovery = ref(null)

const executions = ref([])

const apiAvailable = false // recorder/matching-engine API not proxied in frontend by default
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
            <div class="market-summary">20개 KRW 마켓의 매칭 엔진별 분배 현황을 표시합니다.</div>
        <div class="market-list">
          <template v-if="markets.length === 0">
            <div class="no-data">데이터 연동 예정<br/><small>매칭 엔진 상태 조회 API 연동 후 표시됩니다.</small></div>
          </template>
          <template v-else>
            <div v-for="m in markets" :key="m" class="market-item">{{ m }}</div>
          </template>
        </div>
      </div>
      <div class="top-card-right">
        <div class="badge">
          {{ distributionOk === null ? '확인 전' : (distributionOk ? '중복 없음' : '중복 존재') }}
        </div>
      </div>
    </section>

    <section class="engine-grid">
      <template v-if="!apiAvailable">
        <div class="no-data-panel panel">데이터 연동 예정<br/><small>매칭 엔진 상태 조회 API가 프론트에 연결되어 있지 않습니다.</small></div>
      </template>
      <template v-else-if="engines.length === 0">
        <div class="no-data-panel panel">현재 배정된 매칭 엔진이 없습니다.</div>
      </template>
      <template v-else>
        <div v-for="e in engines" :key="e.engineInstanceId" class="engine-card panel">
          <div class="engine-title">
            <span class="engine-dot" :style="{ backgroundColor: e.color || '#2ed39a' }"></span>
            <strong>{{ e.engineInstanceId }}</strong>
            <span class="engine-status">{{ e.status === 'running' ? '실행 중' : (e.status || '상태 불명') }}</span>
          </div>
          <div class="engine-markets">
            <div v-for="m in e.markets" :key="m.market" class="engine-market">
              <div><strong>{{ m.market }}</strong></div>
              <div class="assigned-at">Assigned: {{ m.assignedAt }}</div>
            </div>
          </div>
        </div>
      </template>
    </section>

    <section class="middle-row">
      <article class="panel match-flow">
        <h4>주문 매칭 순서</h4>
        <template v-if="matchingSteps.length === 0">
          <div class="no-data">데이터 연동 예정<br/><small>매칭 엔진 상태 조회 API 연동 후 표시됩니다.</small></div>
        </template>
        <template v-else>
          <ol class="steps">
            <li v-for="s in matchingSteps" :key="s.step">
              <div class="step-title">{{ s.step }}. {{ s.title }}</div>
              <div class="step-detail">가격: {{ s.price }} · 수량: {{ s.qty }}</div>
            </li>
          </ol>
        </template>
      </article>

      <article class="panel recovery-card">
        <h4>호가창 복구</h4>
        <template v-if="!orderbookRecovery">
          <div class="no-data">데이터 연동 예정<br/><small>매칭 엔진 상태 조회 API 연동 후 표시됩니다.</small></div>
        </template>
        <template v-else>
          <div class="recovery-row">
            <span>재생 완료</span><strong>{{ orderbookRecovery.replayed }} / {{ orderbookRecovery.total }}</strong>
          </div>
          <div class="recovery-row">
            <span>이벤트 누락</span><strong>{{ orderbookRecovery.missing }}건</strong>
          </div>
          <div class="recovery-row">
            <span>복구 시간</span><strong>{{ orderbookRecovery.timeSec }}초</strong>
          </div>
          <div class="recovery-goal">목표: {{ orderbookRecovery.goalSec }}초 이하</div>
          <div class="recovery-status ok">정상</div>
        </template>
      </article>
    </section>

    <section class="panel executions">
      <h4>체결 결과</h4>
      <div class="exec-list">
        <template v-if="executions.length === 0">
          <div class="no-data">데이터 연동 예정<br/><small>매칭 엔진 상태 조회 API 연동 후 표시됩니다.</small></div>
        </template>
        <template v-else>
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
        </template>
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

.assigned-at { color: #9fb0c1; font-size: 12px; margin-top: 4px }

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
