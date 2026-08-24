<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'

// 2026-08-24: 이 화면 전체를 실제 백엔드 데이터로 채웠습니다.
// - 상단 지표 5개 + 처리량 차트: recorder GET /v1/metrics/dashboard
//   (recorder/query/query.go의 DashboardMetrics — 이 화면을 위해 이미
//   만들어져 있던 전용 API, 그라파나가 아니라 이걸 직접 씁니다. 그라파나는
//   인프라 지표 위주라 "접수 TPS/체결 TPS/p99" 같은 애플리케이션 지표는
//   여기 없습니다). 이 핸들러는 Redis 캐시(10초 주기 갱신)를 읽으므로
//   폴링해도 매번 새로 MySQL을 때리지 않습니다(recorder/server.go 주석 참고,
//   과거 RDS CPU 포화 사고가 이 캐시가 생긴 이유).
// - 시스템 구성요소 상태: orderapi GET /v1/system-status (2026-08-24 신규,
//   orderapi/systemstatus.go) — API/Kafka/매칭엔진/MySQL/Redis 각각을 얕게
//   확인합니다.
const POLL_INTERVAL_MS = 10000

const metrics = ref(null)
const systemStatus = ref(null)
const loadError = ref('')

const displayValue = (v, digits = 0) => {
  if (v === null || v === undefined) return '--'
  return Number(v).toLocaleString('ko-KR', { maximumFractionDigits: digits, minimumFractionDigits: digits })
}

const metricCards = computed(() => {
  const m = metrics.value
  return [
    {
      label: '주문 접수 TPS',
      value: m ? displayValue(m.orderAcceptTps, 1) : '--',
      description: '목표 10,000건/초',
      color: '#3478f6',
    },
    {
      label: '체결 TPS',
      value: m ? displayValue(m.executionTps, 1) : '--',
      description: '',
      color: '#2ed39a',
    },
    {
      label: '처리 대기 주문',
      value: m ? displayValue(m.pendingOrders) : '--',
      description: '',
      color: '#ffb84d',
    },
    {
      label: '전체 처리 p99',
      value: m && m.e2eP99SampleCount > 0 ? `${displayValue(m.e2eP99Ms)}ms` : '--',
      description: m && m.e2eP99SampleCount > 0 ? `표본 ${displayValue(m.e2eP99SampleCount)}건` : '목표 500ms 이하',
      color: '#20c8e8',
    },
    {
      label: '실행 중인 Pod',
      value: m ? displayValue(m.runningEnginePods) : '--',
      description: '매칭 엔진',
      color: '#9b7bff',
    },
  ]
})

const statusColor = (status) => (status === 'up' ? '#2ed39a' : '#ff5c7a')
const statusLabel = (status) => (status === 'up' ? '정상' : '장애')

const componentRows = computed(() => {
  if (!systemStatus.value) {
    return ['주문 접수 API', 'Kafka 브로커', '매칭 엔진', 'MySQL', 'Redis 캐시'].map((name) => ({
      name,
      label: '--',
      color: '#8ea2b8',
    }))
  }
  return systemStatus.value.map((c) => ({
    name: c.name,
    label: statusLabel(c.status),
    color: statusColor(c.status),
  }))
})

const allComponentsUp = computed(
  () => !!systemStatus.value && systemStatus.value.every((c) => c.status === 'up')
)

// ---- 처리량 차트 (SVG 라인 차트) ----
// 기본(10분)은 metrics.value.series(위 fetchDashboardMetrics, 캐시된 값,
// 10초 폴링) 그대로 씁니다. 그 외 기간은 GET /v1/metrics/throughput?from=&to=
// (recorder/server.go throughputHandler, 2026-08-24)를 호출합니다 — 이건
// 캐시가 없는 라이브 쿼리라서, 과거 RDS CPU 포화 사고와 같은 패턴을 피하려고
// **자동 폴링 타이머에 절대 안 묶고, 기간을 바꾸거나 새로고침 버튼을 누를
// 때만** 부릅니다(recorder 쪽 주석 참고). 최대 24시간까지만 허용됩니다
// (서버가 그 이상은 400으로 거절).
const rangePresets = [
  { label: '실시간(10분)', minutes: 10 },
  { label: '30분', minutes: 30 },
  { label: '1시간', minutes: 60 },
  { label: '3시간', minutes: 180 },
  { label: '6시간', minutes: 360 },
  { label: '24시간', minutes: 1440 },
]
const selectedRangeMinutes = ref(10)
const isDefaultRange = computed(() => selectedRangeMinutes.value === 10)

const rangeSeries = ref(null)
const rangeLoading = ref(false)
const rangeError = ref('')

async function fetchRangeSeries() {
  if (isDefaultRange.value) return
  rangeLoading.value = true
  rangeError.value = ''
  try {
    const to = new Date()
    const from = new Date(to.getTime() - selectedRangeMinutes.value * 60000)
    const url = `/recorder-api/v1/metrics/throughput?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`
    const res = await fetch(url)
    if (!res.ok) throw new Error(`처리량 조회 실패: ${res.status}`)
    const data = await res.json()
    rangeSeries.value = data.series
  } catch (e) {
    rangeError.value = e instanceof Error ? e.message : String(e)
  } finally {
    rangeLoading.value = false
  }
}

function selectRange(minutes) {
  if (selectedRangeMinutes.value === minutes) return
  selectedRangeMinutes.value = minutes
  rangeError.value = ''
  if (!isDefaultRange.value) fetchRangeSeries()
}

const chartWidth = 600
const chartHeight = 250
const chartPadTop = 14
const chartPadBottom = 28
const chartPadX = 8

function pointsFor(series, key, maxValue) {
  if (series.length < 2) return ''
  const usableHeight = chartHeight - chartPadTop - chartPadBottom
  const usableWidth = chartWidth - chartPadX * 2
  return series
    .map((bucket, i) => {
      const x = chartPadX + (usableWidth * i) / (series.length - 1)
      const ratio = maxValue > 0 ? bucket[key] / maxValue : 0
      const y = chartPadTop + usableHeight * (1 - ratio)
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

const series = computed(() => (isDefaultRange.value ? metrics.value?.series || [] : rangeSeries.value || []))
const seriesMax = computed(() => {
  const s = series.value
  if (s.length === 0) return 0
  return Math.max(1, ...s.map((b) => Math.max(b.orders, b.executions)))
})
const orderPoints = computed(() => pointsFor(series.value, 'orders', seriesMax.value))
const execPoints = computed(() => pointsFor(series.value, 'executions', seriesMax.value))

function toHHMM(bucketStart) {
  const d = new Date(bucketStart)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit', hour12: false })
}

const chartLabels = computed(() => {
  const s = series.value
  if (s.length === 0) return []
  // 10개 버킷 전부 라벨을 붙이면 겹치니, 처음/중간/끝만 남긴다.
  const idxs = [0, Math.floor((s.length - 1) / 2), s.length - 1]
  return [...new Set(idxs)].map((i) => toHHMM(s[i].bucketStart))
})

async function fetchDashboardMetrics() {
  const res = await fetch('/recorder-api/v1/metrics/dashboard')
  if (!res.ok) throw new Error(`지표 조회 실패: ${res.status}`)
  metrics.value = await res.json()
}

async function fetchSystemStatus() {
  const res = await fetch('/order-api/v1/system-status')
  if (!res.ok) throw new Error(`상태 조회 실패: ${res.status}`)
  const data = await res.json()
  systemStatus.value = data.components
}

async function refresh() {
  try {
    await Promise.all([fetchDashboardMetrics(), fetchSystemStatus()])
    loadError.value = ''
  } catch (e) {
    // 값은 마지막으로 성공한 결과를 그대로 유지하고, 에러만 알려준다 —
    // 잠깐의 네트워크 실패로 화면이 전부 '--'로 깜빡이지 않게.
    loadError.value = e instanceof Error ? e.message : String(e)
  }
}

let pollTimer = null

onMounted(() => {
  refresh()
  pollTimer = window.setInterval(refresh, POLL_INTERVAL_MS)
})

onBeforeUnmount(() => {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})
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
      <article v-for="metric in metricCards" :key="metric.label" class="metric-card">
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
            <p>{{ isDefaultRange ? '최근 10분 동안 1분 단위 처리 건수' : '선택한 기간의 처리 건수' }}</p>
          </div>

          <div class="chart-legend">
            <span><i class="order-color"></i>주문</span>
            <span><i class="execution-color"></i>체결</span>
          </div>
        </div>

        <div class="range-picker">
          <button
            v-for="preset in rangePresets"
            :key="preset.minutes"
            class="range-btn"
            :class="{ active: selectedRangeMinutes === preset.minutes }"
            @click="selectRange(preset.minutes)"
          >
            {{ preset.label }}
          </button>
          <button
            v-if="!isDefaultRange"
            class="range-btn range-refresh"
            :disabled="rangeLoading"
            @click="fetchRangeSeries"
          >
            {{ rangeLoading ? '조회 중...' : '새로고침' }}
          </button>
        </div>
        <div v-if="rangeError" class="range-error">{{ rangeError }}</div>

        <div class="chart-placeholder" :class="{ empty: series.length === 0 }">
          <div v-if="series.length === 0" class="empty-center">
            <strong>{{ rangeLoading ? '조회 중...' : '데이터 없음' }}</strong>
            <div class="empty-sub">
              {{ rangeLoading ? '' : '이 기간에 표시할 처리량 데이터가 없습니다.' }}
            </div>
          </div>
          <template v-else>
            <svg
              class="throughput-chart"
              :viewBox="`0 0 ${chartWidth} ${chartHeight}`"
              preserveAspectRatio="none"
            >
              <line
                v-for="n in 4"
                :key="n"
                class="grid-line"
                x1="0"
                :x2="chartWidth"
                :y1="chartPadTop + ((chartHeight - chartPadTop - chartPadBottom) / 4) * n"
                :y2="chartPadTop + ((chartHeight - chartPadTop - chartPadBottom) / 4) * n"
              />
              <polyline class="exec-series" fill="none" stroke="#20c8e8" stroke-width="2" :points="execPoints" />
              <polyline class="order-series" fill="none" stroke="#3478f6" stroke-width="2" :points="orderPoints" />
            </svg>
            <div class="chart-labels">
              <span v-for="(label, i) in chartLabels" :key="i">{{ label }}</span>
            </div>
          </template>
        </div>
      </article>

      <article class="panel status-panel">
        <h3>시스템 구성요소 상태</h3>

        <div v-for="item in componentRows" :key="item.name" class="status-row">
          <span>{{ item.name }}</span>

          <span class="status-value" :style="{ color: item.color }">
            <i :style="{ backgroundColor: item.color }"></i>
            {{ item.label }}
          </span>
        </div>
      </article>
    </section>

    <section v-if="loadError" class="scaling-alert">
      <div class="alert-content">
        <strong>{{ !allComponentsUp && systemStatus ? '일부 구성요소 장애' : '지표 갱신 실패' }}</strong>
        <p>{{ loadError }} — 마지막으로 정상 조회된 값을 계속 표시합니다.</p>
      </div>
    </section>
    <section v-else-if="systemStatus && !allComponentsUp" class="scaling-alert">
      <div class="alert-content">
        <strong>일부 구성요소 장애</strong>
        <p>{{ componentRows.filter((c) => c.label === '장애').map((c) => c.name).join(', ') }} 확인이 필요합니다.</p>
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

.range-picker {
  display: flex;
  margin-top: 14px;
  gap: 8px;
  flex-wrap: wrap;
}

.range-btn {
  padding: 6px 12px;
  color: #8ea2b8;
  background: #11243a;
  border: 1px solid #20344b;
  border-radius: 14px;
  font-size: 11px;
  font-weight: 700;
  cursor: pointer;
}

.range-btn:hover {
  color: #c7d3e0;
}

.range-btn.active {
  color: #0d1b2a;
  background: #3478f6;
  border-color: #3478f6;
}

.range-refresh {
  margin-left: auto;
}

.range-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

.range-error {
  margin-top: 8px;
  color: #ff5c7a;
  font-size: 12px;
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

.empty-center {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  text-align: center;
}

.empty-center strong {
  display: block;
  margin-bottom: 6px;
  color: #c7d3e0;
  font-size: 14px;
}

.empty-sub {
  color: #71869c;
  font-size: 12px;
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

.alert-content strong {
  font-size: 14px;
  color: #ffb84d;
}

.alert-content p {
  margin: 7px 0 0;
  color: #8ea2b8;
  font-size: 12px;
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
