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
// - 시스템 구성요소 상태 + 실시간 처리 흐름(파이프라인 다이어그램, 그라파나
//   "Team1 AI Trader" 대시보드의 것을 이 화면에도 반영): orderapi GET
//   /v1/system-status (2026-08-24, orderapi/systemstatus.go) — API/Kafka/
//   매칭엔진/MySQL/Redis 얕은 헬스체크 + orderapi/matching-engine/recorder
//   파드 레플리카 수(ready/desired). 시세 수집기/AI 트레이더는 별도 헬스체크
//   대상이 없는 배치성 워크로드라 세션 가드(last-run)의 owner=trader로
//   대신 표시합니다. 다이어그램은 SVG + <animateMotion>으로 그리고(순수
//   CSS keyframe보다 곡선 경로를 따라가는 점을 표현하기 쉬움), 정상 구간만
//   점이 흐르고 장애 구간은 회색 점선으로 표시합니다(2026-08-24).
//   **토폴로지 검토(2026-08-24)** — recorder/main.go 확인 결과 기록기는
//   Orders 토픽을 매칭엔진을 거치지 않고 자기 전용 컨슈머 그룹으로 직접
//   구독합니다(그라파나 원본의 "→ 기록기 직접 구독" 라벨과 동일) — 기존
//   일직선 다이어그램은 이 갈림길을 빠뜨리고 있어서, Orders 토픽에서
//   매칭엔진행/기록기 직접구독 두 갈래로 갈라지는 포크 구조로 다시 그렸습니다.
// - 실행 상태(페이퍼 트레이딩/리플레이 진행 여부, 안 돌고 있으면 최근 실행
//   요약): orderapi GET /v1/sessions/last-run.
// - 데이터 정합성 검사: 가장 최근 시뮬레이션(replayengine) 실행 하나의
//   결과만 보여줍니다(실시간 누적 아님) — orderapi GET /v1/sessions/last-run
//   으로 그 실행의 owner/startedAt/endedAt/date를 얻고, 그 구간으로
//   recorder GET /v1/orders/summary + GET /v1/orders/integrity를 조회합니다.
//   "주문 유실"만 recorder 쿼리가 아니라 orderapi GET /v1/jobs/replay-preview
//   의 totalOrders(예정된 주문량)와 orders/summary의 accepted(실제 접수량)
//   차이로 프론트에서 직접 계산합니다 — replayengine은 429 등 Submit 실패를
//   재시도 없이 스킵하므로(FR-18 재현성 유지 목적), 이 차이가 곧 스킵된 주문
//   수입니다(recorder DB 조회가 필요 없음).
const POLL_INTERVAL_MS = 10000

const metrics = ref(null)
const systemStatus = ref(null)
const pods = ref({})
const lastRun = ref(null)
const lastRunFound = ref(false)
const loadError = ref('')

const integrityData = ref(null)
const integrityNote = ref('')

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

// ---- 데이터 정합성 검사 ----
const OK_COLOR = '#2ed39a'
const PROBLEM_COLOR = '#ff5c7a'
const NO_DATA_COLOR = '#8ea2b8'

// 매수/매도 총량 차이는 DECIMAL(24,8) 문자열끼리의 뺄셈이라 부동소수점
// 오차가 아주 미세하게 낄 수 있어(표시용 계산이라 허용), 그 오차보다
// 넉넉히 큰 값만 "불일치"로 판단합니다.
const QTY_DIFF_EPSILON = 1e-6

function formatQtyDiff(v) {
  return Number(v).toLocaleString('ko-KR', { maximumFractionDigits: 8 })
}

const integrityCards = computed(() => {
  const d = integrityData.value
  if (!d) {
    return [
      { label: '순서 역전', value: '--', color: NO_DATA_COLOR },
      { label: '주문 유실', value: '--', color: NO_DATA_COLOR },
      { label: '중복 체결', value: '--', color: NO_DATA_COLOR },
      { label: '매수·매도 총량 불일치', value: '--', color: NO_DATA_COLOR },
    ]
  }
  return [
    {
      label: '순서 역전',
      value: displayValue(d.sequenceReversals),
      color: d.sequenceReversals === 0 ? OK_COLOR : PROBLEM_COLOR,
    },
    {
      label: '주문 유실',
      value: d.orderLoss === null ? '--' : displayValue(d.orderLoss),
      color: d.orderLoss === null ? NO_DATA_COLOR : d.orderLoss === 0 ? OK_COLOR : PROBLEM_COLOR,
    },
    {
      label: '중복 체결',
      value: displayValue(d.duplicateExecutions),
      color: d.duplicateExecutions === 0 ? OK_COLOR : PROBLEM_COLOR,
    },
    {
      label: '매수·매도 총량 불일치',
      value: formatQtyDiff(d.buySellDiff),
      color: d.buySellDiff <= QTY_DIFF_EPSILON ? OK_COLOR : PROBLEM_COLOR,
    },
  ]
})

async function fetchIntegrityCheck() {
  const lastRunRes = await fetch('/order-api/v1/sessions/last-run')
  if (lastRunRes.status === 404) {
    integrityData.value = null
    integrityNote.value = '아직 실행된 시뮬레이션이 없습니다.'
    return
  }
  if (!lastRunRes.ok) throw new Error(`실행 정보 조회 실패: ${lastRunRes.status}`)
  const lastRun = await lastRunRes.json()

  if (lastRun.owner !== 'replayengine') {
    integrityData.value = null
    integrityNote.value = '가장 최근 실행이 리플레이(부하 테스트)가 아니라 정합성 검사를 표시할 수 없습니다.'
    return
  }
  if (!lastRun.endedAt) {
    integrityData.value = null
    integrityNote.value = '리플레이가 아직 진행 중입니다 — 종료 후 결과가 표시됩니다.'
    return
  }

  const from = lastRun.startedAt
  const to = lastRun.endedAt
  const qs = `mode=REPLAY&from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`

  const [previewRes, summaryRes, integrityRes] = await Promise.all([
    lastRun.date
      ? fetch(`/order-api/v1/jobs/replay-preview?date=${encodeURIComponent(lastRun.date)}`)
      : Promise.resolve(null),
    fetch(`/recorder-api/v1/orders/summary?${qs}`),
    fetch(`/recorder-api/v1/orders/integrity?${qs}`),
  ])

  if (!summaryRes.ok) throw new Error(`주문 집계 조회 실패: ${summaryRes.status}`)
  if (!integrityRes.ok) throw new Error(`정합성 검사 조회 실패: ${integrityRes.status}`)

  const summary = await summaryRes.json()
  const integrity = await integrityRes.json()

  // 오래된 실행(이 필드가 생기기 전 기록)이라 date가 없으면 "주문 유실"만
  // 표시를 못 하고 나머지 세 지표는 그대로 보여줍니다.
  let orderLoss = null
  if (previewRes && previewRes.ok) {
    const preview = await previewRes.json()
    orderLoss = Math.max(0, (preview.totalOrders ?? 0) - (summary.accepted ?? 0))
  }

  integrityData.value = {
    orderLoss,
    duplicateExecutions: integrity.duplicateExecutions ?? 0,
    sequenceReversals: integrity.sequenceReversals ?? 0,
    buySellDiff: Math.abs(Number(integrity.buyFilled ?? 0) - Number(integrity.sellFilled ?? 0)),
  }
  integrityNote.value = ''
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
  pods.value = data.pods || {}
}

// 실행 상태 패널 + 실시간 처리 흐름의 "시세 수집기"/"AI 트레이더" 노드가
// 같이 씁니다 — 세션 가드(orderapi/session) 덕분에 이 last-run 하나가 지금
// 뭐가 돌고 있는지(IN_PROGRESS) 또는 가장 최근에 뭐가 끝났는지를 항상
// 정확히 알려줍니다(동시 실행 불가 원칙 참고).
async function fetchLastRun() {
  const res = await fetch('/order-api/v1/sessions/last-run')
  if (res.status === 404) {
    lastRun.value = null
    lastRunFound.value = false
    return
  }
  if (!res.ok) throw new Error(`실행 상태 조회 실패: ${res.status}`)
  lastRun.value = await res.json()
  lastRunFound.value = true
}

async function refresh() {
  try {
    await Promise.all([fetchDashboardMetrics(), fetchSystemStatus(), fetchLastRun()])
    loadError.value = ''
  } catch (e) {
    // 값은 마지막으로 성공한 결과를 그대로 유지하고, 에러만 알려준다 —
    // 잠깐의 네트워크 실패로 화면이 전부 '--'로 깜빡이지 않게.
    loadError.value = e instanceof Error ? e.message : String(e)
  }

  // 정합성 검사는 별도 에러 상태로 분리 — 이게 실패해도 위 지표/상태
  // 패널까지 같이 흔들리면 안 됩니다.
  try {
    await fetchIntegrityCheck()
  } catch (e) {
    integrityData.value = null
    integrityNote.value = e instanceof Error ? e.message : String(e)
  }
}

// ---- 실행 상태 (페이퍼 트레이딩 / 리플레이) ----
const RUN_STATUS_LABELS = {
  IN_PROGRESS: '실행 중',
  COMPLETED: '완료',
  STOPPED: '중지됨',
  FAILED: '실패',
}
const RUN_OWNER_LABELS = {
  trader: '페이퍼 트레이딩',
  replayengine: '리플레이(주문 재생)',
}

const isRunInProgress = computed(() => lastRunFound.value && lastRun.value?.status === 'IN_PROGRESS')
const runTypeLabel = computed(() => RUN_OWNER_LABELS[lastRun.value?.owner] || lastRun.value?.owner || '-')
const runStatusLabel = computed(() => RUN_STATUS_LABELS[lastRun.value?.status] || lastRun.value?.status || '-')

// 실시간 경과 시간 표시용 — POLL_INTERVAL_MS(10초)보다 자주 갱신해야 "실행
// 중" 배지의 경과 시간이 그럴듯하게 흐르므로 별도의 1초 틱을 둔다. 서버에
// 다시 묻지 않고 클라이언트 시계로만 계산하는 값이라 폴링과는 무관하다.
const nowTick = ref(Date.now())
let tickTimer = null

function formatElapsed(startedAt) {
  if (!startedAt) return '-'
  const ms = nowTick.value - new Date(startedAt).getTime()
  if (!Number.isFinite(ms) || ms < 0) return '-'
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  return h > 0 ? `${h}시간 ${m}분 ${s}초` : m > 0 ? `${m}분 ${s}초` : `${s}초`
}

const runElapsed = computed(() => (isRunInProgress.value ? formatElapsed(lastRun.value.startedAt) : '-'))

function formatKST(iso) {
  if (!iso) return '-'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '-'
  return d.toLocaleString('ko-KR', { hour12: false })
}

// ---- 실시간 처리 흐름 (Grafana "Team1 AI Trader - 실시간 메트릭"의
// 파이프라인 다이어그램을 이 화면에도 반영, 2026-08-24) ----
// 시세 수집기/AI 트레이더는 별도 헬스체크 엔드포인트가 없어(둘 다 세션이
// 끝나면 파드/Job 자체가 사라지는 배치성 워크로드라 "지금 떠 있는 파드"로
// 헬스체크할 대상이 없음), 세션 가드(last-run)의 owner=trader 여부로
// 대신합니다 — 페이퍼 트레이딩이 진행 중이면 이 둘도 당연히 진행 중입니다.
const traderRunning = computed(() => isRunInProgress.value && lastRun.value?.owner === 'trader')

function componentStatusOf(name) {
  const c = systemStatus.value?.find((x) => x.name === name)
  return c?.status || null
}
const flowUp = (name) => componentStatusOf(name) === 'up'

// **그라파나 원본 스타일로 재구현 (2026-08-24, 사용자 요청)** — 처음엔
// 토폴로지만 맞춘 SVG 다이어그램이었는데, 사용자가 "그라파나 대시보드의 실제
// 패널(team1-overview, panel id 121)과 똑같은 스타일로" 를 명시적으로
// 요청해서, 그 패널(순수 HTML text 패널 + <canvas>, 손으로 애니메이션 루프를
// 짠 것)의 그리기 로직을 이 컴포넌트로 그대로 옮겼습니다. 다만 그 패널은
// Grafana의 Prometheus 데이터소스 프록시(/api/datasources/proxy/...)를 직접
// 호출해 컨슈머 랙/백프레셔/노드별 파드 배치까지 그리는데, 이 프론트엔드에는
// 그 경로가 없고(대신 recorder/orderapi 자체 REST API만 있음) 이미 갖고
// 있는 데이터(접수/체결 TPS, 파드 ready/desired, 컴포넌트 헬스체크)만으로
// 그릴 수 있는 부분만 옮겼습니다 — 병목(랙/백프레셔) 뱃지와 "워커 노드별
// 배치" 범례는 소스 데이터가 없어 제외했습니다. 노드 상태(정상/장애)는
// 원본엔 없던 요소지만, 기존 헬스체크 정보를 잃지 않으려고 노드 테두리
// 색으로 계속 표시합니다.
const flowNodesDef = [
  { key: 'collector', x: 0.02, label: '시세 수집기', sub: 'backend(배치)', lane: 'trunk' },
  { key: 'trader', x: 0.12, label: 'AI 트레이더', sub: 'trader(Job)', lane: 'trunk' },
  { key: 'orderapi', x: 0.227, label: '접수', sub: 'orderapi', lane: 'trunk' },
  { key: 'orders-topic', x: 0.352, label: 'Orders 토픽', sub: 'Kafka', lane: 'trunk' },
  { key: 'matching', x: 0.585, label: '매칭 엔진', sub: 'matching-engine', lane: 'upper' },
  { key: 'exec-topic', x: 0.728, label: 'Executions 토픽', sub: 'Kafka', lane: 'upper' },
  { key: 'recorder', x: 0.862, label: '기록기', sub: 'recorder', lane: 'trunk' },
  { key: 'mysql', x: 0.965, label: 'MySQL', sub: 'trade_order', lane: 'trunk' },
]
const flowBranchLabels = [
  { x: 0.44, lane: 'upper', text: '→ 매칭 엔진행' },
  { x: 0.44, lane: 'lower', text: '→ 기록기 직접 구독' },
]
const FLOW_SVC_COLOR = { orderapi: '#4a90ff', matching: '#ffb84d', recorder: '#33e6a8' }
const FLOW_SCALE_RANGE = { matching: { min: 2, max: 10 }, recorder: { min: 1, max: 10 } }
const FLOW_BRANCH_START = 0.388
const FLOW_BRANCH_END = 0.424
const FLOW_MERGE_START = 0.813
const FLOW_MERGE_END = 0.849

const flowNodeOk = computed(() => ({
  collector: traderRunning.value,
  trader: traderRunning.value,
  orderapi: flowUp('주문 접수 API'),
  'orders-topic': flowUp('Kafka 브로커'),
  matching: flowUp('매칭 엔진'),
  'exec-topic': flowUp('Kafka 브로커'),
  recorder: flowUp('MySQL'),
  mysql: flowUp('MySQL'),
}))

function flowSmoothstep(e0, e1, x) {
  const t = Math.min(Math.max((x - e0) / (e1 - e0), 0), 1)
  return t * t * (3 - 2 * t)
}
function flowLaneBlend(xFrac) {
  if (xFrac < FLOW_BRANCH_START) return 0
  if (xFrac < FLOW_BRANCH_END) return flowSmoothstep(FLOW_BRANCH_START, FLOW_BRANCH_END, xFrac)
  if (xFrac < FLOW_MERGE_START) return 1
  if (xFrac < FLOW_MERGE_END) return 1 - flowSmoothstep(FLOW_MERGE_START, FLOW_MERGE_END, xFrac)
  return 0
}
function flowLaneCenterFrac(xFrac, lane) {
  if (lane === 'trunk') return 0.5
  const off = lane === 'upper' ? -0.32 : 0.32
  return 0.5 + off * flowLaneBlend(xFrac)
}
function flowScaleFrac(n, svcKey) {
  const r = FLOW_SCALE_RANGE[svcKey] || { min: 1, max: 5 }
  return Math.min(Math.max((n - r.min) / (r.max - r.min), 0), 1)
}
function flowLaneServiceAt(xFrac, lane) {
  if (lane === 'lower') return 'recorder'
  return xFrac < 0.7 ? 'matching' : 'recorder'
}
function flowBandHalfFrac(xFrac, lane, scale) {
  const narrow = 0.045
  const wide = 0.135
  const svcKey = flowLaneServiceAt(xFrac, lane)
  return narrow + (wide - narrow) * flowScaleFrac(scale[svcKey], svcKey)
}
function flowRoundRect(ctx, x, y, w, h, r) {
  ctx.beginPath()
  ctx.moveTo(x + r, y)
  ctx.arcTo(x + w, y, x + w, y + h, r)
  ctx.arcTo(x + w, y + h, x, y + h, r)
  ctx.arcTo(x, y + h, x, y, r)
  ctx.arcTo(x, y, x + w, y, r)
  ctx.closePath()
}
function flowAddSmoothPoints(ctx, pts) {
  if (pts.length < 2) {
    pts.forEach((p, i) => (i === 0 ? ctx.moveTo(p[0], p[1]) : ctx.lineTo(p[0], p[1])))
    return
  }
  ctx.moveTo(pts[0][0], pts[0][1])
  for (let i = 1; i < pts.length - 1; i++) {
    const mx = (pts[i][0] + pts[i + 1][0]) / 2
    const my = (pts[i][1] + pts[i + 1][1]) / 2
    ctx.quadraticCurveTo(pts[i][0], pts[i][1], mx, my)
  }
  ctx.lineTo(pts[pts.length - 1][0], pts[pts.length - 1][1])
}

const flowCanvasEl = ref(null)
let flowCtx = null
let flowDpr = 1
let flowParticles = []
let flowFrameId = null
let flowResizeObserver = null

function flowResizeCanvas() {
  const canvas = flowCanvasEl.value
  if (!canvas) return
  const rect = canvas.parentElement.getBoundingClientRect()
  flowDpr = window.devicePixelRatio || 1
  canvas.width = Math.max(rect.width, 10) * flowDpr
  canvas.height = Math.max(rect.height, 10) * flowDpr
  canvas.style.width = `${rect.width}px`
  canvas.style.height = `${rect.height}px`
  flowCtx.setTransform(flowDpr, 0, 0, flowDpr, 0, 0)
}

function flowSpawnPair(tps, color, mode, w) {
  const rate = Math.max(tps, 0)
  const perFrame = Math.min(Math.sqrt(rate) / 1.9, 3.5)
  let n = Math.floor(perFrame) + (Math.random() < perFrame % 1 ? 1 : 0)
  if (rate < 0.05 && Math.random() < 0.01) n = 1
  for (let i = 0; i < n; i++) {
    const jitter = (Math.random() - 0.5) * 0.75
    const speed = 1.3 + Math.random() * 0.9
    const r = 2.0 + Math.random()
    if (mode === 'both') {
      const startX = 0.227 * w - 4 - Math.random() * 10
      flowParticles.push({ x: startX, lane: 'upper', jitter, speed, color, r })
      flowParticles.push({ x: startX, lane: 'lower', jitter, speed, color, r })
    } else {
      const xJitter = (Math.random() - 0.5) * 14
      flowParticles.push({ x: mode.x + xJitter, lane: mode.lane, jitter, speed, color, r })
    }
  }
}

function flowNodeBoxWidth(ctx, label) {
  return Math.max(ctx.measureText(label).width + 20, 60)
}

function drawFlowFrame() {
  const canvas = flowCanvasEl.value
  if (!canvas || !flowCtx) return
  const ctx = flowCtx
  const w = canvas.width / flowDpr
  const h = canvas.height / flowDpr
  const cy = h * 0.42
  const laneSpan = h * 0.28

  ctx.fillStyle = '#0a1420'
  ctx.fillRect(0, 0, w, h)

  const scale = {
    matching: pods.value?.['matching-engine']?.ready || FLOW_SCALE_RANGE.matching.min,
    recorder: pods.value?.recorder?.ready || FLOW_SCALE_RANGE.recorder.min,
  }
  const steps = 90
  const grad = ctx.createLinearGradient(0, cy - laneSpan * 1.3, 0, cy + laneSpan * 1.3)
  grad.addColorStop(0, 'rgba(52,120,246,0.04)')
  grad.addColorStop(0.5, 'rgba(52,120,246,0.11)')
  grad.addColorStop(1, 'rgba(52,120,246,0.04)')
  for (const lane of ['upper', 'lower']) {
    const pathPts = []
    for (let xi = 0; xi <= steps; xi++) {
      const xf = xi / steps
      const half = flowBandHalfFrac(xf, lane, scale)
      let yTop = cy + (flowLaneCenterFrac(xf, lane) - 0.5) * 2 * laneSpan - half * h
      if (lane === 'lower') yTop = Math.max(yTop, cy)
      pathPts.push([xf * w, yTop])
    }
    for (let xi = steps; xi >= 0; xi--) {
      const xf = xi / steps
      const half = flowBandHalfFrac(xf, lane, scale)
      let yBot = cy + (flowLaneCenterFrac(xf, lane) - 0.5) * 2 * laneSpan + half * h
      if (lane === 'upper') yBot = Math.min(yBot, cy)
      pathPts.push([xf * w, yBot])
    }
    ctx.beginPath()
    flowAddSmoothPoints(ctx, pathPts)
    ctx.closePath()
    ctx.fillStyle = grad
    ctx.fill()
  }

  const envPts = []
  for (let i = 0; i <= steps; i++) {
    const xf = i / steps
    const half = flowBandHalfFrac(xf, 'upper', scale)
    envPts.push([xf * w, cy + (flowLaneCenterFrac(xf, 'upper') - 0.5) * 2 * laneSpan - half * h])
  }
  for (let i = steps; i >= 0; i--) {
    const xf = i / steps
    const half = flowBandHalfFrac(xf, 'lower', scale)
    envPts.push([xf * w, cy + (flowLaneCenterFrac(xf, 'lower') - 0.5) * 2 * laneSpan + half * h])
  }
  ctx.beginPath()
  flowAddSmoothPoints(ctx, envPts)
  ctx.closePath()
  ctx.strokeStyle = 'rgba(159,176,194,0.28)'
  ctx.lineWidth = 1.3
  ctx.stroke()

  const m = metrics.value
  flowSpawnPair(m?.orderAcceptTps || 0, FLOW_SVC_COLOR.orderapi, 'both', w)
  flowSpawnPair(m?.executionTps || 0, FLOW_SVC_COLOR.recorder, { x: 0.585 * w, lane: 'upper' }, w)

  for (let i = flowParticles.length - 1; i >= 0; i--) {
    const p = flowParticles[i]
    p.x += p.speed
    if (p.x > w + 10) {
      flowParticles.splice(i, 1)
      continue
    }
    const xf = Math.min(Math.max(p.x, 0), w) / w
    const laneC = flowLaneCenterFrac(xf, p.lane)
    const half = flowBandHalfFrac(xf, p.lane, scale)
    const py = cy + (laneC - 0.5) * 2 * laneSpan + p.jitter * half * h * 0.85
    ctx.beginPath()
    ctx.fillStyle = p.color
    ctx.shadowColor = p.color
    ctx.shadowBlur = 6
    ctx.arc(p.x, py, p.r, 0, Math.PI * 2)
    ctx.fill()
    ctx.shadowBlur = 0
  }

  ctx.font = '500 10px -apple-system, BlinkMacSystemFont, sans-serif'
  ctx.fillStyle = 'rgba(159,176,194,0.85)'
  ctx.textAlign = 'left'
  for (const bl of flowBranchLabels) {
    const bx = bl.x * w
    const by = cy + (flowLaneCenterFrac(bl.x, bl.lane) - 0.5) * 2 * laneSpan
    const half = flowBandHalfFrac(bl.x, bl.lane, scale) * h
    const ty = bl.lane === 'upper' ? by - half - 12 : by + half + 18
    ctx.fillText(bl.text, bx, ty)
  }

  const okMap = flowNodeOk.value
  for (const nd of flowNodesDef) {
    const nx = nd.x * w
    const ny = cy + (flowLaneCenterFrac(nd.x, nd.lane) - 0.5) * 2 * laneSpan
    ctx.font = '700 12px -apple-system, BlinkMacSystemFont, sans-serif'
    const bw = flowNodeBoxWidth(ctx, nd.label)
    const bh = 34
    const drawCx = Math.min(Math.max(nx, bw / 2 + 4), w - bw / 2 - 4)
    const bx = drawCx - bw / 2
    const by = ny - bh / 2
    const ok = okMap[nd.key]
    ctx.fillStyle = 'rgba(15,26,40,0.9)'
    flowRoundRect(ctx, bx, by, bw, bh, 7)
    ctx.fill()
    ctx.strokeStyle = ok ? 'rgba(159,176,194,0.35)' : '#ff5c7a'
    ctx.lineWidth = ok ? 1.3 : 1.8
    ctx.stroke()
    ctx.fillStyle = '#e8f1fb'
    ctx.textAlign = 'center'
    ctx.font = '700 12px -apple-system, BlinkMacSystemFont, sans-serif'
    ctx.fillText(nd.label, drawCx, ny - 2)
    ctx.fillStyle = '#7f93a8'
    ctx.font = '500 9px -apple-system, BlinkMacSystemFont, sans-serif'
    ctx.fillText(nd.sub, drawCx, ny + 11)
  }

  ctx.fillStyle = 'rgba(10,20,32,0.6)'
  ctx.fillRect(6, 6, 190, 62)
  ctx.fillStyle = '#cfe6ff'
  ctx.font = '600 12px -apple-system, BlinkMacSystemFont, sans-serif'
  ctx.textAlign = 'left'
  ctx.fillText(`● 접수 ${(m?.orderAcceptTps || 0).toFixed(1)}/s`, 14, 24)
  ctx.fillStyle = '#8ff5cf'
  ctx.fillText(`● 체결 ${(m?.executionTps || 0).toFixed(1)}/s`, 14, 42)
  ctx.fillStyle = '#9fb0c2'
  ctx.font = '500 10px -apple-system, BlinkMacSystemFont, sans-serif'
  ctx.fillText(`레플리카: 매칭 ${Math.round(scale.matching)} · 기록기 ${Math.round(scale.recorder)}`, 14, 58)

  ctx.font = '600 10px -apple-system, BlinkMacSystemFont, sans-serif'
  ctx.fillStyle = '#9fb0c2'
  ctx.fillText('워커', 10, h - 8)
  const legendX = 44
  const legendY = h - 11
  ;['orderapi', 'matching', 'recorder'].forEach((svc, li) => {
    const lx = legendX + li * 46
    ctx.beginPath()
    ctx.fillStyle = FLOW_SVC_COLOR[svc]
    ctx.arc(lx, legendY - 3, 3, 0, Math.PI * 2)
    ctx.fill()
    ctx.fillStyle = '#7f93a8'
    ctx.font = '500 9px -apple-system, BlinkMacSystemFont, sans-serif'
    ctx.textAlign = 'left'
    ctx.fillText(svc === 'orderapi' ? '접수' : svc === 'matching' ? '매칭' : '기록', lx + 6, legendY)
  })

  flowFrameId = requestAnimationFrame(drawFlowFrame)
}

function startFlowCanvas() {
  const canvas = flowCanvasEl.value
  if (!canvas) return
  flowCtx = canvas.getContext('2d')
  flowResizeCanvas()
  flowResizeObserver = new ResizeObserver(() => flowResizeCanvas())
  flowResizeObserver.observe(canvas.parentElement)
  flowFrameId = requestAnimationFrame(drawFlowFrame)
}

function stopFlowCanvas() {
  if (flowFrameId !== null) {
    cancelAnimationFrame(flowFrameId)
    flowFrameId = null
  }
  if (flowResizeObserver) {
    flowResizeObserver.disconnect()
    flowResizeObserver = null
  }
}

let pollTimer = null

onMounted(() => {
  refresh()
  pollTimer = window.setInterval(refresh, POLL_INTERVAL_MS)
  tickTimer = window.setInterval(() => {
    nowTick.value = Date.now()
  }, 1000)
  startFlowCanvas()
})

onBeforeUnmount(() => {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  if (tickTimer !== null) {
    clearInterval(tickTimer)
    tickTimer = null
  }
  stopFlowCanvas()
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

    <section class="panel flow-panel">
      <h3>실시간 처리 흐름</h3>
      <div class="flow-canvas-wrap">
        <canvas ref="flowCanvasEl" class="flow-canvas"></canvas>
      </div>
    </section>

    <section class="panel run-status-panel">
      <h3>실행 상태</h3>
      <div v-if="isRunInProgress" class="run-status-row run-active">
        <span class="run-badge running">실행 중</span>
        <div class="run-status-text">
          <strong>{{ runTypeLabel }}</strong>
          <span>시작 {{ formatKST(lastRun.startedAt) }} · 경과 {{ runElapsed }}</span>
        </div>
      </div>
      <div v-else-if="lastRunFound" class="run-status-row">
        <span class="run-badge">종료됨</span>
        <div class="run-status-text">
          <strong>최근 실행: {{ runTypeLabel }} — {{ runStatusLabel }}</strong>
          <span>{{ formatKST(lastRun.startedAt) }} ~ {{ formatKST(lastRun.endedAt) }}</span>
          <span v-if="lastRun.message" class="run-message">{{ lastRun.message }}</span>
        </div>
      </div>
      <div v-else class="run-status-row">
        <span class="run-badge">기록 없음</span>
        <div class="run-status-text"><strong>아직 실행된 적이 없습니다.</strong></div>
      </div>
    </section>

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
      <p v-if="integrityNote" class="integrity-note">{{ integrityNote }}</p>
      <p v-else class="integrity-note">가장 최근 부하 테스트(리플레이) 실행 결과입니다.</p>

      <div class="integrity-grid">
        <div v-for="card in integrityCards" :key="card.label">
          <span>{{ card.label }}</span>
          <strong :style="{ color: card.color }">{{ card.value }}</strong>
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

.flow-panel {
  margin-bottom: 18px;
}

.flow-canvas-wrap {
  position: relative;
  width: 100%;
  height: 280px;
  margin-top: 10px;
  border-radius: 8px;
  overflow: hidden;
}

.flow-canvas {
  width: 100%;
  height: 100%;
  display: block;
}

.run-status-panel {
  margin-bottom: 18px;
}

.run-status-row {
  display: flex;
  margin-top: 14px;
  align-items: center;
  gap: 14px;
}

.run-badge {
  padding: 6px 14px;
  color: #8ea2b8;
  background: #11243a;
  border-radius: 16px;
  font-size: 12px;
  font-weight: 700;
  white-space: nowrap;
}

.run-badge.running {
  color: #0d1b2a;
  background: #2ed39a;
}

.run-status-text {
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-size: 13px;
}

.run-status-text span {
  color: #8ea2b8;
  font-size: 12px;
}

.run-message {
  color: #ffb84d !important;
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
  font-size: 24px;
}

.integrity-note {
  margin: 6px 0 0;
  color: #8ea2b8;
  font-size: 12px;
}
</style>
