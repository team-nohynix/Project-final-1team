<script setup>
import { ref } from 'vue'

const metrics = [
  { label: '주문 접수 TPS', value: null, description: '목표 10,000건/초', color: '#3478f6' },
  { label: '체결 TPS', value: null, description: '', color: '#2ed39a' },
  { label: '처리 대기 주문', value: null, description: '', color: '#ffb84d' },
  { label: '전체 처리 p99', value: null, description: '목표 500ms 이하', color: '#20c8e8' },
  { label: '실행 중인 Pod', value: null, description: '', color: '#9b7bff' },
]

const systemStatus = [
  { name: '주문 접수 API', status: null, color: '#2ed39a' },
  { name: 'Kafka 브로커', status: null, color: '#2ed39a' },
  { name: '매칭 엔진', status: null, color: '#ffb84d' },
  { name: 'PostgreSQL', status: null, color: '#2ed39a' },
  { name: 'Redis 캐시', status: null, color: '#2ed39a' },
]

// Throughput chart data generation (15 minutes, 16 samples)
// No local fake chart data — show placeholder until real metrics integration.
const hasRealMetrics = false

const displayValue = (v) => (v === null || v === undefined ? '--' : v)
</script>

<template>
  <div class="dashboard-view">
    <header class="page-header">
      <div>
        <h2>시스템 종합 현황</h2>
        <p>실시간 주문 처리 상태와 Kubernetes 자동 확장 현황</p>
      </div>

      <div class="live-badge">
        <span class="live-dot"></span>
        실시간
      </div>
    </header>

    <section class="metrics-grid">
      <article v-for="metric in metrics" :key="metric.label" class="metric-card">
        <div class="metric-title">
          <span>{{ metric.label }}</span>
          <span class="metric-dot" :style="{ backgroundColor: metric.color }"></span>
        </div>

        <strong>{{ metric.value === null || metric.value === undefined ? '--' : metric.value }}</strong>

        <p :style="{ color: metric.color }">
          {{ metric.description }}
        </p>
      </article>
    </section>

    <section class="dashboard-grid">
      <article class="panel throughput-panel">
        <div class="panel-header">
          <div>
            <h3>주문·체결 처리량</h3>
            <p>최근 15분 동안 초당 처리 건수</p>
          </div>

          <div class="chart-legend">
            <span><i class="order-color"></i>주문</span>
            <span><i class="execution-color"></i>체결</span>
          </div>
        </div>

        <div class="chart-placeholder empty">
          <div class="empty-center">
            <strong>데이터 연동 예정</strong>
            <div class="empty-sub">실제 인프라 및 백엔드 연동 후 데이터가 표시됩니다.</div>
          </div>
        </div>
      </article>

      <article class="panel status-panel">
        <h3>시스템 구성요소 상태</h3>

        <div v-for="item in systemStatus" :key="item.name" class="status-row">
          <span>{{ item.name }}</span>

          <span class="status-value" :style="{ color: item.color }">
            <i :style="{ backgroundColor: item.color }"></i>
            {{ item.status }}
          </span>
        </div>
      </article>
    </section>

    <section class="scaling-alert">
      <div class="alert-content empty-center">
        <strong>데이터 연동 예정</strong>
        <p>실제 인프라 및 백엔드 연동 후 확장 상태가 표시됩니다.</p>
      </div>
    </section>

    <section class="integrity-panel">
      <h3>데이터 정합성 검사</h3>

      <div class="integrity-grid">
        <div>
          <span>순서 역전</span>
          <strong>{{ '--' }}</strong>
        </div>
        <div>
          <span>주문 유실</span>
          <strong>{{ '--' }}</strong>
        </div>
        <div>
          <span>중복 체결</span>
          <strong>{{ '--' }}</strong>
        </div>
        <div>
          <span>매수·매도 총량 불일치</span>
          <strong>{{ '--' }}</strong>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  margin-bottom: 24px;
  padding-bottom: 22px;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid #20344b;
}

.page-header h2 {
  margin: 0 0 7px;
  font-size: 28px;
}

.page-header p,
.panel-header p {
  margin: 0;
  color: #8ea2b8;
  font-size: 13px;
}

.live-badge {
  display: flex;
  padding: 8px 13px;
  align-items: center;
  gap: 8px;
  color: #2ed39a;
  background: #11243a;
  border-radius: 18px;
  font-size: 12px;
  font-weight: 700;
}

.metrics-grid {
  display: grid;
  grid-template-columns: repeat(5, minmax(170px, 1fr));
  gap: 14px;
}

.metric-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: #8ea2b8;
  font-size: 12px;
  font-weight: 700;
}

.metric-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
}

.metric-card strong {
  display: block;
  margin-top: 17px;
  font-size: 27px;
}

.metric-card p {
  margin: 8px 0 0;
  font-size: 12px;
  font-weight: 700;
}

.dashboard-grid {
  display: grid;
  grid-template-columns: minmax(600px, 2fr) minmax(320px, 1fr);
  gap: 16px;
  margin-top: 18px;
}

.panel h3,
.integrity-panel h3 {
  margin: 0;
  font-size: 16px;
}

.panel-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
}

.panel-header h3 {
  margin-bottom: 7px;
}

.chart-legend {
  display: flex;
  gap: 14px;
  color: #8ea2b8;
  font-size: 11px;
}

.chart-legend span {
  display: flex;
  align-items: center;
  gap: 6px;
}

.chart-legend i {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.order-color {
  background: #3478f6;
}

.execution-color {
  background: #20c8e8;
}

.chart-placeholder {
  position: relative;
  height: 250px;
  margin-top: 22px;
  overflow: hidden;
  background:
    linear-gradient(#20344b 1px, transparent 1px),
    linear-gradient(90deg, #20344b 1px, transparent 1px);
  background-size:
    100% 50px,
    25% 100%;
  border-radius: 10px;
}

.throughput-chart {
  width: 100%;
  height: 100%;
  display: block;
}

.grid-line {
  stroke: #20344b;
  stroke-width: 0.6;
  stroke-dasharray: 1.5 2.5;
}

.throughput-chart .order-series path[stroke] {
  filter: drop-shadow(0 0 0 rgba(0, 0, 0, 0));
}

.throughput-chart .exec-series path[stroke] {
  filter: drop-shadow(0 0 0 rgba(0, 0, 0, 0));
}

.chart-labels {
  position: absolute;
  right: 14px;
  bottom: 10px;
  left: 14px;
  display: flex;
  justify-content: space-between;
  color: #71869c;
  font-size: 10px;
}

.status-panel {
  min-height: 330px;
}

.status-row {
  display: flex;
  padding: 19px 0;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid #20344b;
  font-size: 13px;
}

.status-row:last-child {
  border-bottom: 0;
}

.status-value {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 12px;
  font-weight: 700;
}

.status-value i {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.scaling-alert {
  display: flex;
  margin-top: 18px;
  padding: 20px;
  align-items: center;
  gap: 17px;
  background: #0d1b2a;
  border: 1px solid #4b3a1e;
  border-radius: 15px;
}

.alert-icon {
  display: grid;
  width: 50px;
  height: 50px;
  place-items: center;
  color: #ffb84d;
  background: #11243a;
  border-radius: 12px;
  font-size: 25px;
}

.alert-content strong {
  font-size: 14px;
}

.alert-content p {
  margin: 7px 0 0;
  color: #8ea2b8;
  font-size: 12px;
}

.scaling-badge {
  margin-left: auto;
  padding: 8px 12px;
  color: #ffb84d;
  background: #11243a;
  border-radius: 16px;
  font-size: 11px;
  font-weight: 700;
}

.integrity-panel {
  margin-top: 18px;
  padding: 20px;
}

.integrity-grid {
  display: grid;
  margin-top: 20px;
  grid-template-columns: repeat(4, 1fr);
}

.integrity-grid div {
  display: flex;
  padding: 0 22px;
  flex-direction: column;
  gap: 9px;
  border-right: 1px solid #20344b;
}

.integrity-grid div:first-child {
  padding-left: 0;
}

.integrity-grid div:last-child {
  border-right: 0;
}

.integrity-grid span {
  color: #8ea2b8;
  font-size: 12px;
}

.integrity-grid strong {
  color: #2ed39a;
  font-size: 24px;
}
</style>
