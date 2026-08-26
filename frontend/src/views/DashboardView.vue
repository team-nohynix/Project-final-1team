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
// 2026-08-26: "실시간 처리 흐름" 캔버스를 제외한 나머지 데이터는 1초로
// 새로고침한다. 이 값은 recorder GET /v1/metrics/dashboard의 Redis 캐시
// 갱신 주기(10초, recorder/metrics.go)보다 짧아서 매 틱마다 항상 새로운
// 값을 받는 건 아니지만(캐시가 갱신되기 전엔 같은 값이 반복됨), 그 자체는
// Redis 캐시 조회라 가볍다 — 무거운 데이터 정합성 검사만 별도 주기로 뺐다
// (INTEGRITY_POLL_INTERVAL_MS 주석 참고).
const POLL_INTERVAL_MS = 1000

const metrics = ref(null)
const systemStatus = ref(null)
const pods = ref({})
const lastRun = ref(null)
const lastRunFound = ref(false)
const previousRun = ref(null)
const previousRunFound = ref(false)
const previousRun2 = ref(null)
const previousRun2Found = ref(false)
const loadError = ref('')

const integrityData = ref(null)
const integrityNote = ref('')

// orderapi GET /v1/cluster-metrics (clustermetrics.go) — 그라파나
// team1-overview 대시보드가 이미 쓰는 PromQL 그대로 재사용(2026-08-24,
// 사용자 제안). PROMETHEUS_URL 미설정 환경(로컬 등)에서는 404가 나므로
// clusterMetricsError로 별도 표시하고 나머지 화면에는 영향 없게 한다.
const clusterMetrics = ref(null)
const clusterMetricsError = ref('')

async function fetchClusterMetrics() {
  const res = await fetch('/order-api/v1/cluster-metrics')
  if (!res.ok) throw new Error(`클러스터 지표 조회 실패: ${res.status}`)
  clusterMetrics.value = await res.json()
  recordClusterHistory(clusterMetrics.value)
}

// 클러스터 현황 수치(활성 노드 수 등)는 절대값만으론 "지금 늘고 있는지 줄고
// 있는지" 알기 어렵다 — pendingOrdersTrend(위)와 같은 이유. 다만 클러스터는
// 폴링 주기(10초)가 짧아 "직전 폴링 대비"로는 노이즈가 너무 커서, 실제로
// "1분 전 대비"를 보여주려면 스냅샷 이력을 따로 들고 있어야 한다.
const CLUSTER_HISTORY_MAX_AGE_MS = 90000
const clusterMetricsHistory = ref([])

function recordClusterHistory(c) {
  if (!c) return
  const now = Date.now()
  clusterMetricsHistory.value = [...clusterMetricsHistory.value, { t: now, c }].filter(
    (e) => now - e.t <= CLUSTER_HISTORY_MAX_AGE_MS
  )
}

// 이력 중 "1분 전 시점에 가장 가까웠던" 스냅샷을 찾는다 — 정확히 60000ms 전
// 샘플이 없을 수 있으니(폴링 10초 간격 기준 오차는 최대 ±10초), 60초 이전
// 중 가장 최신인 것을 쓴다. 아직 1분치 이력이 안 쌓였으면 null.
function clusterSnapshotOneMinAgo() {
  const target = Date.now() - 60000
  let candidate = null
  for (const e of clusterMetricsHistory.value) {
    if (e.t <= target) candidate = e
  }
  return candidate ? candidate.c : null
}

function clusterDeltaText(current, previous) {
  if (current === null || current === undefined || previous === null || previous === undefined) return ''
  const delta = current - previous
  if (delta === 0) return '1분 전과 동일'
  const arrow = delta > 0 ? '▲' : '▼'
  return `${arrow}${displayValue(Math.abs(delta))} (1분 전 대비)`
}

const displayValue = (v, digits = 0) => {
  if (v === null || v === undefined) return '--'
  return Number(v).toLocaleString('ko-KR', { maximumFractionDigits: digits, minimumFractionDigits: digits })
}

const pendingOrdersTrend = computed(() => {
  const m = metrics.value
  const prev = previousPendingOrders.value
  if (!m || prev === null) return ''
  const delta = m.pendingOrders - prev
  if (delta === 0) return '변화 없음'
  const arrow = delta > 0 ? '▲' : '▼'
  return `${arrow} ${displayValue(Math.abs(delta))} (${delta > 0 ? '밀리는 중' : '따라잡는 중'})`
})

// 각 카드가 재는 시간 범위가 서로 달라서(TPS/p99는 롤링 창, 처리 대기 주문은
// 시간창 없이 전체 누적) 라벨에 그 범위를 명시한다 — 안 그러면 "이게 지금
// 진행 중인 테스트 얘기인지, 최근 몇 분인지" 알 수 없다는 문의를 받았다
// (2026-08-25). TPS/p99는 한 발 더 나가서, recorder가 응답에 실어주는
// tpsWindowSource/e2eWindowSource(realtime|last_run)를 보고 라벨 자체를
// 상황에 맞게 바꾼다 — 진행 중인 테스트가 있으면 "최근 1분/5분"(방금 몇
// 초/분의 실시간 처리량이 의미 있음), 없으면 "지난 실행 기준"(recorder가
// 마지막 실행의 [시작,종료) 구간 전체로 다시 계산해줌 — "최근 1분"이라고
// 해놓고 아무 일도 없어 0만 뜨는 것보다 훨씬 유용함).
function windowLabel(source, recentSuffix) {
  return source === 'last_run' ? '지난 실행 기준' : `최근 ${recentSuffix}`
}

const metricCards = computed(() => {
  const m = metrics.value
  const tpsWin = windowLabel(m?.tpsWindowSource, '1분')
  const e2eWin = windowLabel(m?.e2eWindowSource, '5분')
  return [
    {
      label: `주문 접수 TPS (${tpsWin})`,
      value: m ? displayValue(m.orderAcceptTps, 1) : '--',
      description: '',
      color: '#3478f6',
    },
    {
      label: `체결 TPS (${tpsWin})`,
      value: m ? displayValue(m.executionTps, 1) : '--',
      description: '',
      color: '#2ed39a',
    },
    {
      label: '처리 대기 주문 (전체 누적)',
      value: m ? displayValue(m.pendingOrders) : '--',
      description: pendingOrdersTrend.value,
      color: '#ffb84d',
    },
    {
      label: `전체 처리 p99 (${e2eWin})`,
      value: m && m.e2eP99SampleCount > 0 ? `${displayValue(m.e2eP99Ms)}ms` : '--',
      description: m && m.e2eP99SampleCount > 0 ? `표본 ${displayValue(m.e2eP99SampleCount)}건` : '',
      color: '#20c8e8',
    },
    {
      label: '실행 중인 Pod (현재)',
      value: m ? displayValue(m.runningEnginePods) : '--',
      description: '',
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

// 드롭된(429 백프레셔로 거절된) 주문 — orderapi GET /v1/dropped-orders.
// recorder의 접수/체결 시계열과 달리 MySQL이 아니라 orderapi 프로세스
// 메모리에서 나오는 값이라(droppedmetrics.go 주석 참고) 별도 fetch가
// 필요합니다 — bucketStart로 병합해서 같은 차트에 세 번째 선으로 그립니다.
// 기본(10분) 구간은 다른 두 선과 같은 리듬(10초 폴링, refresh() 참고)으로
// 같이 갱신하고, 커스텀 구간은 그쪽과 마찬가지로 사용자가 기간을 바꿀 때만
// 다시 부릅니다 — 한쪽만 실시간이고 한쪽만 스냅샷이면 같은 차트 안에서
// 시간 구간이 어긋나 보여 혼란스럽습니다.
const droppedSeriesDefault = ref({})
const droppedSeriesRange = ref({})

async function fetchDroppedSeries(minutes) {
  const to = new Date()
  const from = new Date(to.getTime() - minutes * 60000)
  const url = `/order-api/v1/dropped-orders?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`
  const res = await fetch(url)
  if (!res.ok) throw new Error(`드롭된 주문 조회 실패: ${res.status}`)
  const data = await res.json()
  const map = {}
  for (const b of data.series || []) map[b.bucketStart] = b.dropped
  return map
}

async function fetchDroppedSeriesDefault() {
  droppedSeriesDefault.value = await fetchDroppedSeries(10)
}

async function fetchRangeSeries() {
  if (isDefaultRange.value) return
  rangeLoading.value = true
  rangeError.value = ''
  try {
    const to = new Date()
    const from = new Date(to.getTime() - selectedRangeMinutes.value * 60000)
    const url = `/recorder-api/v1/metrics/throughput?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`
    const [res, droppedMap] = await Promise.all([
      fetch(url),
      fetchDroppedSeries(selectedRangeMinutes.value).catch(() => ({})),
    ])
    if (!res.ok) throw new Error(`처리량 조회 실패: ${res.status}`)
    const data = await res.json()
    rangeSeries.value = data.series
    droppedSeriesRange.value = droppedMap
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

// 그라파나 "① 주문 처리 현황" 행의 상태별 건수 4개 — recorder
// DashboardMetrics.ordersByStatus 그대로(그라파나의 recorder_orders_by_status
// Prometheus 게이지도 recorder가 이 값을 그대로 내보내는 것이라 같은 데이터).
// 전부 recorder/query/query.go의 ordersByStatusWindow(최근 10분) 안에서
// "접수된" 주문 기준이라, 핵심 지표의 "처리 대기 주문"(전체 누적, 시간창
// 없음)과 절대 숫자가 안 맞는 게 정상입니다 — 언뜻 같은 걸 나타내는 걸로
// 보이기 쉬워서(2026-08-25 사용자 문의) 패널 부제로 시간창을 명시합니다.
//
// 매칭엔진 호가창 잔량은 여기서 뺐습니다 — 주문 개수가 아니라 매칭엔진이
// 메모리에 들고 있는 실시간 오더북 크기(내부 구현 디테일)라, 사용자가 보는
// "주문이 얼마나 밀려있나"와는 다른 질문에 답하는 지표입니다. 클러스터
// 현황(엔지니어용 Prometheus 지표 섹션)으로 옮겼습니다.
const orderStatusCards = computed(() => {
  const byStatus = metrics.value?.ordersByStatus || {}
  return [
    { label: '처리해야 할 (ACCEPTED)', value: displayValue(byStatus.ACCEPTED ?? 0), color: '#ff5c7a' },
    { label: '처리 중 (PARTIALLY_FILLED)', value: displayValue(byStatus.PARTIALLY_FILLED ?? 0), color: '#ffd23f' },
    { label: '처리 완료 (FILLED)', value: displayValue(byStatus.FILLED ?? 0), color: '#2ed39a' },
    { label: '취소됨 (CANCELED)', value: displayValue(byStatus.CANCELED ?? 0), color: '#8ea2b8' },
  ]
})

// 그라파나 "개요" 행 중 orderapi/매칭엔진/기록기/시세수집기/AI트레이더
// 상태는 이미 흐름도로 옮겼으니(2026-08-24), 여기는 그 나머지 — 활성
// 노드 수/backend 전체 실행 Pod/오토스케일링(/최대)/파드 재시작 누적.
const clusterCards = computed(() => {
  const c = clusterMetrics.value
  const prev = clusterSnapshotOneMinAgo()
  return [
    {
      label: '활성 노드 수',
      value: c ? displayValue(c.activeNodes) : '--',
      delta: c ? clusterDeltaText(c.activeNodes, prev?.activeNodes ?? null) : '',
    },
    {
      label: '실행 중인 Pod',
      value: c ? displayValue(c.runningPodsBackend) : '--',
      delta: c ? clusterDeltaText(c.runningPodsBackend, prev?.runningPodsBackend ?? null) : '',
    },
    // 원래 "주문 처리 현황"에 있었던 카드 — 내부 구현 디테일(매칭엔진 메모리
    // 오더북 크기)이라 여기 엔지니어용 섹션으로 옮겼습니다(2026-08-25).
    {
      label: '매칭엔진 호가창 잔량',
      value: c ? displayValue(c.matchingBookSize) : '--',
      delta: c ? clusterDeltaText(c.matchingBookSize, prev?.matchingBookSize ?? null) : '',
    },
  ]
})

// 오토스케일링 현황 — 예전엔 clusterCards 안에 "N / max" 텍스트로만 있던 걸
// 막대그래프로 바꿨다(2026-08-26 요청). matching/recorder는 KEDA
// ScaledObject의 실제 min/max가 있어 current/max 비율이 그대로 막대 높이가
// 되지만, Karpenter 노드 수는 API에 대응하는 "max" 값 자체가 없다(Karpenter는
// KEDA처럼 레플리카 상한을 선언하는 게 아니라 NodePool의 CPU 총량 예산으로
// 상한을 표현하므로 — infra/k8s/karpenter/nodepool-backend.yaml 참고). 그래서
// Karpenter 막대는 "max N"을 사칭하지 않고, matching의 max(레플리카 1개당
// 노드 1대에 가깝게 튜닝돼 있음)를 시각적 눈금으로만 빌려 쓴다 — 실제 상한
// 표기가 아니므로 라벨에 "(max ...)"를 붙이지 않는다.
const autoscalingBars = computed(() => {
  const c = clusterMetrics.value
  if (!c) return []
  const prev = clusterSnapshotOneMinAgo()
  const matchingMax = c.autoscaling.matching.max || 1
  const recorderMax = c.autoscaling.recorder.max || 1
  const karpenterScale = matchingMax || 1
  const pct = (current, max) => Math.max(4, Math.min(100, Math.round((current / max) * 100)))
  return [
    {
      key: 'matching',
      label: 'matching-engine',
      maxLabel: `max ${displayValue(matchingMax)}`,
      current: c.autoscaling.matching.current,
      percent: pct(c.autoscaling.matching.current, matchingMax),
      delta: clusterDeltaText(c.autoscaling.matching.current, prev?.autoscaling?.matching?.current ?? null),
    },
    {
      key: 'recorder',
      label: 'recorder',
      maxLabel: `max ${displayValue(recorderMax)}`,
      current: c.autoscaling.recorder.current,
      percent: pct(c.autoscaling.recorder.current, recorderMax),
      delta: clusterDeltaText(c.autoscaling.recorder.current, prev?.autoscaling?.recorder?.current ?? null),
    },
    {
      key: 'karpenter',
      label: 'Karpenter 노드',
      maxLabel: '',
      current: c.autoscaling.karpenterNodes,
      percent: pct(c.autoscaling.karpenterNodes, karpenterScale),
      delta: clusterDeltaText(c.autoscaling.karpenterNodes, prev?.autoscaling?.karpenterNodes ?? null),
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
const droppedByBucket = computed(() => (isDefaultRange.value ? droppedSeriesDefault.value : droppedSeriesRange.value))
// recorder 시계열(series)에 orderapi 시계열(droppedByBucket)을 bucketStart로
// 병합 — 둘이 서로 다른 서비스/저장소에서 나오지만 버킷 정의(분 단위,
// "2006-01-02T15:04:00Z" UTC 포맷)가 같아서 키로 그대로 맞출 수 있다.
const seriesWithDropped = computed(() =>
  series.value.map((b) => ({ ...b, dropped: droppedByBucket.value[b.bucketStart] || 0 }))
)
const seriesMax = computed(() => {
  const s = seriesWithDropped.value
  if (s.length === 0) return 0
  return Math.max(1, ...s.map((b) => Math.max(b.orders, b.executions, b.dropped)))
})
const orderPoints = computed(() => pointsFor(seriesWithDropped.value, 'orders', seriesMax.value))
const execPoints = computed(() => pointsFor(seriesWithDropped.value, 'executions', seriesMax.value))
const droppedPoints = computed(() => pointsFor(seriesWithDropped.value, 'dropped', seriesMax.value))

function toHHMM(bucketStart) {
  const d = new Date(bucketStart)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'Asia/Seoul' })
}

const chartLabels = computed(() => {
  const s = series.value
  if (s.length === 0) return []
  // 10개 버킷 전부 라벨을 붙이면 겹치니, 처음/중간/끝만 남긴다.
  const idxs = [0, Math.floor((s.length - 1) / 2), s.length - 1]
  return [...new Set(idxs)].map((i) => toHHMM(s[i].bucketStart))
})

// 처리 대기 주문의 절대값만으로는 "334,654가 많은 건지 적은 건지" 판단이 안
// 된다는 피드백(2026-08-25)에 따라, 직전 폴링 대비 증감 추세를 같이 보여준다
// — 밀리는 중(늘어남)인지 따라잡는 중(줄어듦)인지가 실질적으로 더 유용하다.
const previousPendingOrders = ref(null)

async function fetchDashboardMetrics() {
  const res = await fetch('/recorder-api/v1/metrics/dashboard')
  if (!res.ok) throw new Error(`지표 조회 실패: ${res.status}`)
  previousPendingOrders.value = metrics.value?.pendingOrders ?? null
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

// previous-run/previous-run-2는 최근 3개를 한 줄로 보여달라는 요청(2026-08-25)
// 지원 — 404(그 순번까지 실행이 없었음)는 정상 상태라 조용히 "없음"으로 둔다.
async function fetchPreviousRun() {
  const res = await fetch('/order-api/v1/sessions/previous-run')
  if (res.status === 404) {
    previousRun.value = null
    previousRunFound.value = false
    return
  }
  if (!res.ok) throw new Error(`직전 실행 조회 실패: ${res.status}`)
  previousRun.value = await res.json()
  previousRunFound.value = true
}

async function fetchPreviousRun2() {
  const res = await fetch('/order-api/v1/sessions/previous-run-2')
  if (res.status === 404) {
    previousRun2.value = null
    previousRun2Found.value = false
    return
  }
  if (!res.ok) throw new Error(`전전 실행 조회 실패: ${res.status}`)
  previousRun2.value = await res.json()
  previousRun2Found.value = true
}

// refresh()가 1초마다 도는 것과 별개로, 한 번의 호출이 1초보다 오래 걸리면
// (네트워크 지연 등) 다음 타이머 틱과 겹쳐 요청이 계속 쌓일 수 있다 — 이전
// 호출이 아직 진행 중이면 이번 틱은 건너뛴다.
let refreshInFlight = false

async function refresh() {
  if (refreshInFlight) return
  refreshInFlight = true
  try {
    await Promise.all([
      fetchDashboardMetrics(),
      fetchSystemStatus(),
      fetchLastRun(),
      fetchPreviousRun(),
      fetchPreviousRun2(),
      // 드롭된 주문 조회 실패는 다른 카드까지 '--'로 만들 정도로 치명적이지
      // 않으니 별도로 삼킨다 — 값은 그냥 이전 걸 유지.
      fetchDroppedSeriesDefault().catch(() => {}),
      fetchClusterMetrics()
        .then(() => {
          clusterMetricsError.value = ''
        })
        .catch((e) => {
          clusterMetricsError.value = e instanceof Error ? e.message : String(e)
        }),
    ])
    loadError.value = ''
  } catch (e) {
    // 값은 마지막으로 성공한 결과를 그대로 유지하고, 에러만 알려준다 —
    // 잠깐의 네트워크 실패로 화면이 전부 '--'로 깜빡이지 않게.
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    refreshInFlight = false
  }
}

// 데이터 정합성 검사(GET /v1/orders/integrity)는 recorder/query.go가 Redis
// 캐시 없이 매번 MySQL을 직접 훑는 무거운 쿼리다 — 실측 16.7초(2026-08-26,
// REPLAY 1시간 구간 기준). 위 refresh()와 같은 주기로 돌리면(예전엔 10초)
// 쿼리 자체가 그보다 오래 걸려 다음 호출과 계속 겹쳐 쌓이면서 RDS에 상시
// 부하를 준다 — 과거 RDS CPU 포화 사고와 같은 패턴. 게다가 이 지표는 "가장
// 최근 실행 결과"라 그 실행이 끝나기 전까진 값 자체가 안 바뀌므로, 자주
// 다시 물어볼 이유도 없다. refresh()의 1초 주기와 분리해 훨씬 느린 별도
// 타이머로 두고, 이전 호출이 안 끝났으면 겹쳐 쏘지 않는다.
const INTEGRITY_POLL_INTERVAL_MS = 60000
let integrityCheckInFlight = false

async function pollIntegrityCheck() {
  if (integrityCheckInFlight) return
  integrityCheckInFlight = true
  try {
    await fetchIntegrityCheck()
  } catch (e) {
    integrityData.value = null
    integrityNote.value = e instanceof Error ? e.message : String(e)
  } finally {
    integrityCheckInFlight = false
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

function formatKST(iso) {
  if (!iso) return '-'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '-'
  // 'ko-KR'은 로케일(숫자/구두점 표기 방식)일 뿐 시간대가 아니다 — timeZone을
  // 명시하지 않으면 브라우저의 시스템 시간대를 그대로 쓴다. 함수 이름이
  // "KST"인 이상 뷰어의 로컬 설정과 무관하게 항상 Asia/Seoul로 강제해야
  // 한다(2026-08-25, 실제 KST보다 9시간 밀려 표시되는 걸 실측으로 발견).
  return d.toLocaleString('ko-KR', { hour12: false, timeZone: 'Asia/Seoul' })
}

// 최근 3개 실행을 한 줄로(2026-08-25 요청) — last-run/previous-run/
// previous-run-2 중 실제로 있는 것만 순서대로 담는다. 진행 중(경과 시간 실시간
// 표시)일 수 있는 건 항상 첫 번째(last-run)뿐이다 — previous-run(-2)은 이미
// 새 실행에 밀려난 "무조건 끝난" 기록이라서.
const recentRunCards = computed(() => {
  const slots = [
    { record: lastRun.value, found: lastRunFound.value },
    { record: previousRun.value, found: previousRunFound.value },
    { record: previousRun2.value, found: previousRun2Found.value },
  ]
  return slots
    .filter((s) => s.found)
    .map((s, i) => {
      const owner = RUN_OWNER_LABELS[s.record.owner] || s.record.owner || '-'
      // "몇 번째 전" 슬롯(i>0)은 정의상 그 뒤로 새 실행이 이미 (한 번 이상)
      // 시작됐다는 뜻이라 — 이 프로젝트의 세션 배타성 보장상 그 시점에 이미
      // 끝나 있었어야 정상입니다 — 저장된 status가 여전히 IN_PROGRESS라면
      // "지금 실행 중"이 아니라 "정상 반납 없이 죽었다"(예: OOMKilled로
      // SIGKILL돼 반납 코드가 아예 못 돔, 2026-08-25 실측)는 뜻입니다.
      // "실행 중"이라고 보여주면 사실과 다르므로 별도 상태로 구분합니다.
      const zombie = i > 0 && s.record.status === 'IN_PROGRESS'
      const status = zombie ? '미종료 (비정상 종료 추정)' : RUN_STATUS_LABELS[s.record.status] || s.record.status || '-'
      const inProgress = !zombie && s.record.status === 'IN_PROGRESS'
      // 정상/비정상 종료 여부 + 비정상이면 원인(사용자가 중지 버튼을 눌렀는지,
      // 오류로 끊겼는지)까지 구분해서 보여준다(2026-08-26 요청). 아직 실행
      // 중(inProgress)이면 결론이 안 났으니 비워둔다. STOPPED은 이 프로젝트에서
      // 항상 사용자의 "중지" 버튼(POST .../stop)을 통해서만 나오는 상태라
      // "사용자 중단"으로 단정할 수 있다 — FAILED는 그 외의 오류 종료.
      let outcome = ''
      if (!inProgress) {
        if (zombie) outcome = '비정상 종료 — 오류로 추정 (정상 반납 없이 응답 끊김)'
        else if (s.record.status === 'COMPLETED') outcome = '정상 종료'
        else if (s.record.status === 'STOPPED') outcome = '비정상 종료 — 사용자 중단'
        else if (s.record.status === 'FAILED') outcome = '비정상 종료 — 오류'
      }
      return {
        inProgress,
        zombie,
        owner,
        status,
        outcome,
        startedAt: s.record.startedAt,
        endedAt: s.record.endedAt,
        message: s.record.message,
        speed: s.record.speed,
      }
    })
})

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
const FLOW_SVC_COLOR = { orderapi: '#4a90ff', matching: '#ffb84d', recorder: '#33e6a8' }
const FLOW_SCALE_RANGE = { matching: { min: 2, max: 10 }, recorder: { min: 1, max: 10 } }
const FLOW_BRANCH_START = 0.388
const FLOW_BRANCH_END = 0.424
const FLOW_MERGE_START = 0.813
const FLOW_MERGE_END = 0.849

const flowNodeOk = computed(() => ({
  // 시세 수집기(collector)는 트레이더/리플레이 세션과 무관하게 항상 떠
  // 있는 배치 서비스라(2026-08-25 실측), 세션 상태(traderRunning) 대신
  // orderapi가 실제로 그 서비스에 도달 가능한지 확인한 결과(systemstatus.go의
  // "시세 수집기" 컴포넌트)를 쓴다.
  collector: flowUp('시세 수집기'),
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
  // 0.32였을 때 매칭엔진 박스가 트렁크(아래 줄)에 너무 가깝게 붙어 보인다는
  // 지적(2026-08-25, 실제 스크린샷 픽셀 좌표로 위아래 여백이 대칭인 것까진
  // 확인했지만 — 대칭이어도 트렁크와의 절대 분리 폭 자체가 부족해 보임) —
  // 위/아래 레인을 트렁크에서 더 멀리 띄운다.
  const off = lane === 'upper' ? -0.42 : 0.42
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
  const own = narrow + (wide - narrow) * flowScaleFrac(scale[svcKey], svcKey)
  // 트렁크 구간(분기 전/병합 후, flowLaneBlend=0)에서는 upper/lower 둘 다
  // cy에 겹쳐 그려지는데(flowLaneCenterFrac이 둘 다 0.5를 줌), 각자 다른
  // 서비스 스케일(매칭 vs 기록기)을 그대로 쓰면 두께가 서로 달라서 —
  // drawFlowFrame의 클램프(upper는 cy 아래쪽만, lower는 cy 위쪽만 그리게
  // 자름)와 겹치면서 전체 트렁크 통로가 더 두꺼운 쪽(대개 매칭, 레플리카가
  // 많음) 방향으로만 치우친 모양이 된다 — 트렁크 박스 4개(시세수집기~Orders
  // 토픽)가 아래로 치우쳐 보이던 진짜 원인이었다(2026-08-25 실측, 매칭
  // 캔버스 위쪽 절반만 채워지고 기록기 스케일이 작아 아래쪽은 거의 안
  // 채워짐). blend가 0일 땐 두 서비스 중 더 두꺼운 쪽으로 맞춰 대칭
  // 단일 통로처럼 보이게 하고, 완전히 갈라진 뒤(blend=1)엔 원래대로 각자의
  // 서비스 스케일을 쓴다.
  const otherKey = svcKey === 'matching' ? 'recorder' : 'matching'
  const other = narrow + (wide - narrow) * flowScaleFrac(scale[otherKey], otherKey)
  const shared = Math.max(own, other)
  const blend = flowLaneBlend(xFrac)
  return shared + (own - shared) * blend
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

function flowNodeBoxWidth(ctx, label, sub) {
  // label(굵게 12px)만 재고 sub(가늘게 9px, "matching-engine"처럼 label보다
  // 긴 영문 표기가 많음)는 안 재서, sub가 박스 폭을 넘어 삐져나오는 문제가
  // 있었다(2026-08-25) — 두 폰트 각각으로 재서 더 넓은 쪽에 맞춘다.
  ctx.font = '700 12px -apple-system, BlinkMacSystemFont, sans-serif'
  const labelWidth = ctx.measureText(label).width
  ctx.font = '500 9px -apple-system, BlinkMacSystemFont, sans-serif'
  const subWidth = sub ? ctx.measureText(sub).width : 0
  return Math.max(Math.max(labelWidth, subWidth) + 20, 60)
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

  // 안쪽(서로 마주보는) 경계 — 트렁크 구간(분기 전/병합 후)처럼 두 레인이
  // 이미 cy에 붙어있는 곳까지 그리면 가로지르는 seam 선처럼 보이므로,
  // laneBlend(xf) > 0으로 실제 갈라진 구간에서만 그린다(그라파나 원본과 동일).
  {
    const upperPts = []
    const lowerPts = []
    for (let gi = 0; gi <= steps; gi++) {
      const gxf = gi / steps
      const uC = flowLaneCenterFrac(gxf, 'upper')
      const uH = flowBandHalfFrac(gxf, 'upper', scale)
      const lC = flowLaneCenterFrac(gxf, 'lower')
      const lH = flowBandHalfFrac(gxf, 'lower', scale)
      const uInner = Math.min(cy + (uC - 0.5) * 2 * laneSpan + uH * h, cy)
      const lInner = Math.max(cy + (lC - 0.5) * 2 * laneSpan - lH * h, cy)
      if (flowLaneBlend(gxf) > 0.001) {
        upperPts.push([gxf * w, uInner])
        lowerPts.push([gxf * w, lInner])
      } else {
        upperPts.push(null)
        lowerPts.push(null)
      }
    }
    const strokeBroken = (pts) => {
      ctx.beginPath()
      let run = []
      const flushRun = () => {
        if (run.length >= 2) flowAddSmoothPoints(ctx, run)
        else if (run.length === 1) ctx.moveTo(run[0][0], run[0][1])
        run = []
      }
      pts.forEach((pt) => {
        if (!pt) {
          flushRun()
          return
        }
        run.push(pt)
      })
      flushRun()
      ctx.strokeStyle = 'rgba(159,176,194,0.28)'
      ctx.lineWidth = 1.3
      ctx.stroke()
    }
    strokeBroken(upperPts)
    strokeBroken(lowerPts)
  }

  // orderAcceptTps/executionTps는 유휴 상태일 때 "지난 실행 기준" 평균으로
  // 대체된다(windowLabel 주석 참고) — 핵심 지표 카드에는 그게 0보다 유용하지만,
  // 이 파티클 애니메이션은 "지금 실제로 흐르고 있는가"를 보여주는 용도라
  // 지난 실행 평균으로 스폰하면 아무 작업도 안 도는데 트래픽이 흐르는 것처럼
  // 보인다(2026-08-25, 사용자 리포트). tpsWindowSource==='realtime'일 때만
  // (진행 중인 테스트가 있을 때만) 실제 값으로 스폰한다.
  const m = metrics.value
  const isRealtime = m?.tpsWindowSource === 'realtime'
  flowSpawnPair(isRealtime ? m?.orderAcceptTps || 0 : 0, FLOW_SVC_COLOR.orderapi, 'both', w)
  flowSpawnPair(isRealtime ? m?.executionTps || 0 : 0, FLOW_SVC_COLOR.recorder, { x: 0.585 * w, lane: 'upper' }, w)

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

  // "→ 매칭 엔진행"/"→ 기록기 직접 구독" 화살표 라벨은 지웠다(2026-08-25) —
  // 박스 자체가 이미 어디로 가는지("매칭 엔진"/"기록기") 보여줘서 중복이었고,
  // 레플리카 수가 많아 통로가 두꺼울 때 캔버스 밖으로 밀려 잘리는 문제의
  // 근본 원인이기도 했다.

  const okMap = flowNodeOk.value
  for (const nd of flowNodesDef) {
    const nx = nd.x * w
    const ny = cy + (flowLaneCenterFrac(nd.x, nd.lane) - 0.5) * 2 * laneSpan
    const bw = flowNodeBoxWidth(ctx, nd.label, nd.sub)
    // 통로 두께가 레플리카 수만큼 넓어지면(scale.matching/recorder) 고정
    // 34px 박스가 그 안에서 상대적으로 작아 보이는 문제(2026-08-25 사용자
    // 지적) — 박스 높이를 그 지점 통로 반두께에 비례해서 같이 키운다.
    // 30~48px로 clamp — 통로가 가장 얇을 때도 라벨이 읽히고, 가장 두꺼울
    // 때도 박스가 통로를 넘어설 만큼 과하게 커지지 않게.
    const bandHalfPx = flowBandHalfFrac(nd.x, nd.lane, scale) * h
    const bh = Math.min(Math.max(bandHalfPx * 1.1, 30), 48)
    const drawCx = Math.min(Math.max(nx, bw / 2 + 4), w - bw / 2 - 4)
    const bx = drawCx - bw / 2
    const by = ny - bh / 2
    const ok = okMap[nd.key]
    ctx.fillStyle = 'rgba(15,26,40,0.9)'
    flowRoundRect(ctx, bx, by, bw, bh, 7)
    ctx.fill()
    ctx.strokeStyle = ok ? '#2ed39a' : '#ff5c7a'
    ctx.lineWidth = ok ? 1.6 : 1.8
    ctx.stroke()
    ctx.fillStyle = '#e8f1fb'
    ctx.textAlign = 'center'
    ctx.font = '700 12px -apple-system, BlinkMacSystemFont, sans-serif'
    ctx.fillText(nd.label, drawCx, ny - 2)
    ctx.fillStyle = '#7f93a8'
    ctx.font = '500 9px -apple-system, BlinkMacSystemFont, sans-serif'
    ctx.fillText(nd.sub, drawCx, ny + 11)
  }

  // "레플리카: 매칭 X · 기록기 Y"는 아래 "클러스터 현황" 패널에 이미 있는
  // 정보라 중복이었고, "Redis 캐시" 상태는 매칭엔진 박스 모서리의 점(바로
  // 위에서 그림, 이번에 더 잘 보이게 고침)이 이미 보여주고 있어서 텍스트로
  // 또 안 적어도 됨(2026-08-25 지적) — 이 박스엔 여기서만 볼 수 있는
  // 실시간 TPS만 남긴다.
  ctx.fillStyle = 'rgba(10,20,32,0.6)'
  ctx.fillRect(6, 6, 150, 40)
  ctx.fillStyle = '#cfe6ff'
  ctx.font = '600 11px -apple-system, BlinkMacSystemFont, sans-serif'
  ctx.textAlign = 'left'
  ctx.fillText(`● 접수 ${(isRealtime ? m?.orderAcceptTps || 0 : 0).toFixed(1)}/s`, 14, 20)
  ctx.fillStyle = '#8ff5cf'
  ctx.fillText(`● 체결 ${(isRealtime ? m?.executionTps || 0 : 0).toFixed(1)}/s`, 14, 35)

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
let integrityPollTimer = null

onMounted(() => {
  refresh()
  pollTimer = window.setInterval(refresh, POLL_INTERVAL_MS)
  pollIntegrityCheck()
  integrityPollTimer = window.setInterval(pollIntegrityCheck, INTEGRITY_POLL_INTERVAL_MS)
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
  if (integrityPollTimer !== null) {
    clearInterval(integrityPollTimer)
    integrityPollTimer = null
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
      <div v-if="recentRunCards.length" class="run-status-list">
        <div
          v-for="(card, i) in recentRunCards"
          :key="i"
          class="run-status-row"
          :class="{ 'run-active': card.inProgress }"
        >
          <span class="run-badge" :class="{ running: card.inProgress, zombie: card.zombie }">
            {{ card.inProgress ? '실행 중' : card.zombie ? '미종료' : '종료됨' }}
          </span>
          <div class="run-status-text">
            <strong>{{ card.owner }}{{ card.inProgress ? '' : ` — ${card.status}` }}</strong>
            <span v-if="card.speed">{{ card.speed }}배속</span>
            <span v-if="card.inProgress">시작 {{ formatKST(card.startedAt) }} · 경과 {{ formatElapsed(card.startedAt) }}</span>
            <span v-else>{{ formatKST(card.startedAt) }} ~ {{ formatKST(card.endedAt) }}</span>
            <span v-if="card.message" class="run-message">{{ card.message }}</span>
          </div>
        </div>
      </div>
      <div v-else class="run-status-row">
        <span class="run-badge">기록 없음</span>
        <div class="run-status-text"><strong>아직 실행된 적이 없습니다.</strong></div>
      </div>
    </section>

    <section class="panel metrics-panel">
      <h3>핵심 지표</h3>
      <div class="metrics-grid">
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
      </div>

      <h4 class="order-status-subtitle">주문 처리 현황</h4>
      <p class="cluster-note">최근 10분간 접수된 주문의 상태별 건수입니다.</p>
      <div class="stat-grid stat-grid-4">
        <div v-for="card in orderStatusCards" :key="card.label">
          <span>{{ card.label }}</span>
          <strong :style="{ color: card.color }">{{ card.value }}</strong>
        </div>
      </div>
    </section>

    <section class="panel cluster-panel">
      <h3>클러스터 현황</h3>
      <p v-if="clusterMetricsError" class="cluster-note">{{ clusterMetricsError }}</p>
      <div class="stat-grid stat-grid-3">
        <div v-for="card in clusterCards" :key="card.label">
          <span>{{ card.label }}</span>
          <strong>{{ card.value }}</strong>
          <em v-if="card.delta" class="cluster-delta">{{ card.delta }}</em>
        </div>
      </div>

      <h4 class="autoscaling-subtitle">
        오토스케일링 현황 (KEDA 레플리카 수 / 최대)
        <span
          class="info-icon"
          title="matching-engine/recorder는 KEDA ScaledObject의 실제 min/max 기준입니다. Karpenter 노드는 KEDA처럼 선언된 상한이 없어(NodePool의 CPU 예산으로 상한을 표현) 막대 눈금만 matching-engine의 max를 빌려 쓰고, 'max' 수치는 표기하지 않습니다."
        >ⓘ</span>
      </h4>
      <div v-if="autoscalingBars.length" class="autoscaling-bars">
        <div v-for="bar in autoscalingBars" :key="bar.key" class="autoscaling-bar-col">
          <div class="autoscaling-bar-value">{{ displayValue(bar.current) }}</div>
          <div class="autoscaling-bar-track">
            <div class="autoscaling-bar-fill" :class="`bar-${bar.key}`" :style="{ height: bar.percent + '%' }"></div>
          </div>
          <div class="autoscaling-bar-label">{{ bar.label }}<template v-if="bar.maxLabel"> ({{ bar.maxLabel }})</template></div>
          <em v-if="bar.delta" class="cluster-delta">{{ bar.delta }}</em>
        </div>
      </div>
      <div v-else class="cluster-note">클러스터 지표를 불러오는 중...</div>
    </section>

    <section class="throughput-section">
      <article class="panel throughput-panel">
        <div class="panel-header">
          <div>
            <h3>주문·체결·드롭 처리량</h3>
            <p>{{ isDefaultRange ? '최근 10분 동안 1분 단위 처리 건수' : '선택한 기간의 처리 건수' }}</p>
          </div>

          <div class="chart-legend">
            <span><i class="order-color"></i>주문</span>
            <span><i class="execution-color"></i>체결</span>
            <span><i class="dropped-color"></i>드롭됨(429)</span>
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
              <polyline class="dropped-series" fill="none" stroke="#ff8a3d" stroke-width="2" :points="droppedPoints" />
              <polyline class="exec-series" fill="none" stroke="#20c8e8" stroke-width="2" :points="execPoints" />
              <polyline class="order-series" fill="none" stroke="#3478f6" stroke-width="2" :points="orderPoints" />
            </svg>
            <div class="chart-labels">
              <span v-for="(label, i) in chartLabels" :key="i">{{ label }}</span>
            </div>
          </template>
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

    <section class="panel integrity-panel">
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
  /* 320px는 화살표 라벨이 캔버스 밖으로 잘리는 걸 막으려고 늘렸던 값인데
     (2026-08-25), 그 라벨 자체를 없애면서 더 이상 필요 없어졌고 — 대신
     lower 레인(박스 없이 그냥 흐르기만 함)과 범례 사이에 빈 공간만
     많이 남는다는 지적을 받아 다시 줄인다. */
  height: 260px;
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

.run-status-list {
  display: flex;
  flex-wrap: wrap;
  margin-top: 14px;
  gap: 12px;
}

.run-status-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: 1 1 260px;
  min-width: 0;
  padding: 12px 14px;
  background: #0e1b2b;
  border: 1px solid #16283d;
  border-radius: 10px;
}

.run-status-row.run-active {
  background: rgba(46, 211, 154, 0.14);
  border-color: rgba(46, 211, 154, 0.5);
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

.run-badge.zombie {
  color: #ffb84d;
  background: rgba(255, 184, 77, 0.15);
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

.metrics-panel {
  margin-top: 18px;
}

.metrics-panel h3 {
  margin: 0;
  font-size: 16px;
}

.metrics-grid {
  display: grid;
  margin-top: 20px;
  grid-template-columns: repeat(5, minmax(150px, 1fr));
}

.metric-card {
  padding: 0 20px;
  border-right: 1px solid #20344b;
}

.metric-card:first-child {
  padding-left: 0;
}

.metric-card:last-child {
  border-right: 0;
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
  font-variant-numeric: tabular-nums;
}

.metric-card p {
  margin: 8px 0 0;
  font-size: 12px;
  font-weight: 700;
}

.throughput-section {
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

.dropped-color {
  background: #ff8a3d;
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

.cluster-panel {
  margin-top: 18px;
}

.cluster-panel h3 {
  margin: 0;
  font-size: 16px;
}

.order-status-subtitle {
  margin: 24px 0 0;
  font-size: 13px;
  color: #9fb0c2;
}

.cluster-note {
  margin: 6px 0 0;
  color: #8ea2b8;
  font-size: 12px;
}

.cluster-delta {
  font-style: normal;
  font-size: 11px;
  color: #9fb0c2;
}

.stat-grid {
  display: grid;
  margin-top: 20px;
}

.stat-grid-4 {
  grid-template-columns: repeat(4, 1fr);
}

.stat-grid-6 {
  grid-template-columns: repeat(6, 1fr);
}

.stat-grid-3 {
  grid-template-columns: repeat(3, 1fr);
}

.autoscaling-subtitle {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 24px 0 0;
  font-size: 13px;
  color: #9fb0c2;
}

.info-icon {
  color: #8ea2b8;
  font-size: 13px;
  cursor: help;
}

.autoscaling-bars {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 24px;
  margin-top: 20px;
}

.autoscaling-bar-col {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
}

.autoscaling-bar-value {
  font-size: 20px;
  font-weight: 700;
  color: #2ed39a;
}

.autoscaling-bar-track {
  width: 100%;
  height: 110px;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 8px;
  display: flex;
  align-items: flex-end;
  overflow: hidden;
}

.autoscaling-bar-fill {
  width: 100%;
  background: #2ed39a;
  border-radius: 2px;
  transition: height 0.4s ease;
}

.autoscaling-bar-fill.bar-karpenter {
  background: #7fd858;
}

.autoscaling-bar-label {
  color: #9fb0c2;
  font-size: 12px;
  text-align: center;
}

.stat-grid div {
  display: flex;
  padding: 0 20px;
  flex-direction: column;
  gap: 9px;
  border-right: 1px solid #20344b;
}

.stat-grid div:first-child {
  padding-left: 0;
}

.stat-grid div:last-child {
  border-right: 0;
}

.stat-grid span {
  color: #8ea2b8;
  font-size: 12px;
}

.stat-grid strong {
  font-size: 22px;
  font-variant-numeric: tabular-nums;
}

</style>
