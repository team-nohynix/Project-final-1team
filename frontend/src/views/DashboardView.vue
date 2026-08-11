<script setup>
import { ref } from 'vue'

const metrics = [
  {
    label: '주문 접수 TPS',
    value: '9,842',
    description: '목표 10,000건/초',
    color: '#3478f6',
  },
  {
    label: '체결 TPS',
    value: '9,611',
    description: '체결률 97.6%',
    color: '#2ed39a',
  },
  {
    label: '처리 대기 주문',
    value: '12,480',
    description: 'KEDA 확장 감지',
    color: '#ffb84d',
  },
  {
    label: '전체 처리 p99',
    value: '428ms',
    description: '목표 500ms 이하',
    color: '#20c8e8',
  },
  {
    label: '실행 중인 Pod',
    value: '8 / 20',
    description: 'KEDA 4 → 8',
    color: '#9b7bff',
  },
]

const systemStatus = [
  { name: '주문 접수 API', status: '정상', color: '#2ed39a' },
  { name: 'Kafka 브로커', status: '3 / 3', color: '#2ed39a' },
  { name: '매칭 엔진', status: '확장 중', color: '#ffb84d' },
  { name: 'PostgreSQL', status: '정상', color: '#2ed39a' },
  { name: 'Redis 캐시', status: '정상', color: '#2ed39a' },
]

// Throughput chart data generation (15 minutes, 16 samples)
const samples = 16
const orderPoints = ref([])
const execPoints = ref([])

const generatePoints = () => {
  const o = []
  const e = []
  for (let i = 0; i < samples; i++) {
    // base waviness using sin for smooth ups/downs
    const t = i / (samples - 1)
    const base = 50 + Math.sin(t * Math.PI * 2.2) * 18 // primary waveform
    const noise = Math.sin(t * 7.3) * 6
    const order = Math.max(8, Math.min(92, base + noise + Math.cos(t * 3.1) * 6))
    // execution follows similar but offset to avoid exact overlap
    const exec = Math.max(6, Math.min(94, order - 6 + Math.sin(t * 4.1) * 10))
    o.push(order)
    e.push(exec)
  }
  orderPoints.value = o
  execPoints.value = e
}

generatePoints()

const normPoints = (p) => (p && p.value ? p.value : p)

const buildLinePath = (pointsRef) => {
  const pts = normPoints(pointsRef)
  if (!pts || pts.length === 0) return ''
  const parts = pts.map((v, i) => {
    const x = (i / (pts.length - 1)) * 100
    const y = 100 - v
    return `${x.toFixed(2)},${y.toFixed(2)}`
  })
  return `M ${parts.join(' L ')}`
}

const buildAreaPath = (pointsRef) => {
  const line = buildLinePath(pointsRef)
  if (!line) return ''
  // close to baseline (y=100)
  return `${line} L 100,100 L 0,100 Z`
}

const lastPointCoord = (pointsRef) => {
  const pts = normPoints(pointsRef)
  if (!pts || pts.length === 0) return { x: 0, y: 100 }
  const i = pts.length - 1
  const x = (i / (pts.length - 1)) * 100
  const y = 100 - pts[i]
  return { x: x.toFixed(2), y: y.toFixed(2) }
}
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

        <strong>{{ metric.value }}</strong>

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

        <div class="chart-placeholder">
          <svg
            class="throughput-chart"
            viewBox="0 0 100 100"
            preserveAspectRatio="none"
            xmlns="http://www.w3.org/2000/svg"
            role="img"
            aria-label="주문·체결 처리량 그래프"
          >
            <defs>
              <linearGradient id="grad-order" x1="0" x2="0" y1="0" y2="1">
                <stop offset="0%" stop-color="#3478f6" stop-opacity="0.25" />
                <stop offset="100%" stop-color="#3478f6" stop-opacity="0" />
              </linearGradient>

              <linearGradient id="grad-exec" x1="0" x2="0" y1="0" y2="1">
                <stop offset="0%" stop-color="#20c8e8" stop-opacity="0.25" />
                <stop offset="100%" stop-color="#20c8e8" stop-opacity="0" />
              </linearGradient>
            </defs>

            <!-- grid lines (4 dashed horizontal lines) -->
            <g class="grid-lines">
              <line
                v-for="i in 4"
                :key="i"
                class="grid-line"
                :x1="0"
                :x2="100"
                :y1="i * 20"
                :y2="i * 20"
              />
            </g>

            <!-- order path and area -->
            <g class="series order-series">
              <path :d="buildAreaPath(orderPoints)" fill="url(#grad-order)" stroke="none" />
              <path
                :d="buildLinePath(orderPoints)"
                fill="none"
                stroke="#3478f6"
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
              <circle
                :cx="lastPointCoord(orderPoints).x"
                :cy="lastPointCoord(orderPoints).y"
                r="1.4"
                fill="#3478f6"
              />
            </g>

            <!-- exec path and area -->
            <g class="series exec-series">
              <path :d="buildAreaPath(execPoints)" fill="url(#grad-exec)" stroke="none" />
              <path
                :d="buildLinePath(execPoints)"
                fill="none"
                stroke="#20c8e8"
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
              <circle
                :cx="lastPointCoord(execPoints).x"
                :cy="lastPointCoord(execPoints).y"
                r="1.4"
                fill="#20c8e8"
              />
            </g>
          </svg>

          <div class="chart-labels">
            <span>15분 전</span>
            <span>10분 전</span>
            <span>5분 전</span>
            <span>현재</span>
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
      <div class="alert-icon">↗</div>

      <div class="alert-content">
        <strong>KEDA 매칭 엔진 자동 확장 진행 중</strong>
        <p>Kafka Consumer Lag 기준 초과 · 매칭 엔진 Pod 4개에서 8개로 확장</p>
      </div>

      <span class="scaling-badge">확장 중</span>
    </section>

    <section class="integrity-panel">
      <h3>데이터 정합성 검사</h3>

      <div class="integrity-grid">
        <div>
          <span>순서 역전</span>
          <strong>0</strong>
        </div>
        <div>
          <span>주문 유실</span>
          <strong>0</strong>
        </div>
        <div>
          <span>중복 체결</span>
          <strong>0</strong>
        </div>
        <div>
          <span>매수·매도 총량 불일치</span>
          <strong>0</strong>
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
