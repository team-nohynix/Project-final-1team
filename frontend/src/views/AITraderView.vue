<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount, onMounted } from 'vue'

// Defaults
const defaultScenarioName = 'BTC 급등락 페이퍼 트레이딩'

const scenarioName = ref(defaultScenarioName)
const selectedDate = ref('') // YYYY-MM-DD — 백엔드가 이 날짜의 KST 00:00~다음 날 KST 00:00 구간을 수집
// 재생 배속 옵션 (프론트에서 선택만 제공)
const speed = ref(60)

// 날짜 입력의 상한값 (오늘) — 미래 날짜 선택 방지
const formatDateYYYYMMDD = (date: Date) => {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}
const todayDate = formatDateYYYYMMDD(new Date())

// UI messages
const infoMessage = ref('')
const errorMessage = ref('')

// 미종결 주문 일괄 정리 (2026-08-20) — 과거 여러 세션이 누적으로 남긴
// ACCEPTED/PARTIALLY_FILLED 주문을 한 번에 취소한다. 세션 종료 자동 정리
// (orderapi/sessioncleanup.go)는 "방금 끝난 세션 몫"만 처리하므로, 그 전에
// 쌓인 백로그는 이 버튼으로 수동 처리해야 한다.
const cleanupLoading = ref(false)
const cleanupMessage = ref('')
const cleanupIsError = ref(false)

// 매칭엔진 호가창 잔량 초기화 (2026-08-21) — 위 "일괄 정리"와는 다른 문제를
// 겨냥한다. 그건 DB(recorder) 기준 미종결 주문을 취소하는 거라, 매칭엔진이
// 크래시 등으로 그 취소를 못 받거나 스냅샷 저장에 실패하면 DB는 깨끗해져도
// 매칭엔진 자신의 Redis 스냅샷 + 각 파드 메모리에는 미체결 주문이 그대로
// 남는다(orderapi/adminreset.go 참고). 이 버튼은 그 잔량만 지운다.
const bookResetLoading = ref(false)
const bookResetMessage = ref('')
const bookResetIsError = ref(false)

// 과거 시세 수집 상태 (백엔드 API 미확정 — 프론트 상태만 준비)
const collectionStatus = ref('idle') // 'idle' | 'collecting' | 'completed' | 'failed'
// 백엔드 응답 예정 필드: 수집 날짜 / 수집 마켓 수 / 수집 성공 마켓 수 / 수집 실패 마켓 수
// collectedData will also hold the raw results array when completed
const collectedData = ref(null)

// Job ID returned by POST /v1/collect (used for polling)
const collectJobId = ref('')
// 진행률 — GET /v1/collect/{jobId} 응답의 completed/total(2026-08-12 백엔드에
// 추가됨)을 그대로 보관. 폴링 전이거나 아직 한 마켓도 안 끝났으면 0/0.
const collectCompleted = ref(0)
const collectTotal = ref(0)
const collectProgressPercent = computed(() => {
  if (!collectTotal.value) return 0
  return Math.round((collectCompleted.value / collectTotal.value) * 100)
})

// The date that was requested for collection (set at POST time). This is preserved while collecting/completed.
const requestedCollectDate = ref('')

// polling control
let pollTimerId = null
let pollInFlight = false

// SessionStorage key
const STORAGE_KEY = 'truss:aiTrader:v1'

// Save/restore guards
let restoringFromStorage = false

// Persist relevant state to sessionStorage
function saveStateToSession() {
  try {
    const payload = {
      scenarioName: scenarioName.value,
      selectedDate: selectedDate.value,
      speed: speed.value,
      // collection
      collectJobId: collectJobId.value,
      collectionStatus: collectionStatus.value,
      requestedCollectDate: requestedCollectDate.value,
      collectedData: collectedData.value,
      collectCompleted: collectCompleted.value,
      collectTotal: collectTotal.value,
      // execution / paper trading
      executionStatus: executionStatus.value,
      paperTradingResult: paperTradingResult.value,
      runStartedAt: runStartedAt.value,
      runEndedAt: runEndedAt.value,
      executionError: executionError.value,
      previousRunId: previousRunId.value,
      awaitingNewRun: awaitingNewRun.value,
      // stop request state
      stopRequested: stopRequested.value,
      stopRequestInFlight: stopRequestInFlight.value,
      // timestamp to help debugging
      savedAt: new Date().toISOString(),
    }
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(payload))
  } catch (e) {
    // ignore storage errors
    console.warn('Failed to save AITrader state to sessionStorage', e)
  }
}

function clearSessionStorage() {
  try {
    sessionStorage.removeItem(STORAGE_KEY)
  } catch (e) {
    console.warn('Failed to clear sessionStorage', e)
  }
}

function loadStateFromSession() {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw)
    return parsed
  } catch (e) {
    // corrupted JSON — clear and return null
    console.warn('Failed to parse stored AITrader state, clearing', e)
    try { sessionStorage.removeItem(STORAGE_KEY) } catch (_) {}
    return null
  }
}

const collectionStatusInfo = computed(() => {
  const statusMap = {
    idle: { label: '수집 요청 전', className: 'status-idle' },
    collecting: { label: '수집 요청 중...', className: 'status-collecting' },
    completed: { label: '수집 완료', className: 'status-completed' },
    failed: { label: '수집 실패', className: 'status-failed' },
  }
  return statusMap[collectionStatus.value]
})

// 수집 날짜가 선택되었을 때만 수집 요청 가능
const canRequestCollection = computed(() => !!selectedDate.value)

const collectionButtonLabel = computed(() => {
  if (collectionStatus.value === 'collecting') return '시세 수집 중...'
  if (collectionStatus.value === 'failed') return '다시 요청'
  if (collectionStatus.value === 'completed') return '수집 완료'
  return '시세 수집 요청'
})

const collectionButtonDisabled = computed(() => {
  if (collectionStatus.value === 'collecting') return true
  if (collectionStatus.value === 'completed') return true
  return !canRequestCollection.value
})

const collectionTargetDate = computed(() => {
  // Prefer server-provided date in collectedData when available, otherwise the requestedCollectDate
  return (collectedData.value && collectedData.value.date) || requestedCollectDate.value || ''
})

// Paper trading order summary type and typed accessor
interface OrderSummary { accepted: number; filled: number; unfilled: number }
const typedPaperTradingResult = computed<OrderSummary | null>(() => {
  const val = paperTradingResult.value as any
  if (!val) return null
  // best-effort: expect numeric fields
  return {
    accepted: Number(val.accepted || 0),
    filled: Number(val.filled || 0),
    unfilled: Number(val.unfilled || 0),
  }
})

const collectionRangeDisplay = computed(() => {
  const date = collectionTargetDate.value
  if (!date) return '-'
  try {
    const d = new Date(`${date}T00:00:00+09:00`)
    const next = new Date(d.getTime() + 24 * 60 * 60 * 1000)
    const pad = (n) => String(n).padStart(2, '0')
    const fmt = (dt) => `${dt.getFullYear()}-${pad(dt.getMonth() + 1)}-${pad(dt.getDate())} ${pad(dt.getHours())}:${pad(dt.getMinutes())}`
    return `${fmt(d)} ~ ${fmt(next)} KST`
  } catch (e) {
    return '-'
  }
})

// targetThroughput removed (no longer used)

// 페이퍼 트레이딩 시작에 필요한 필수 입력값이 모두 채워졌는지 여부
// (목표 처리량은 totalOrders/generationTime으로부터 계산되므로 두 값이 유효하면 함께 충족됨)
const canCreate = computed(() => {
  if (!scenarioName.value) return false
  if (!selectedDate.value) return false
  return true
})

// 페이퍼 트레이딩 시작 버튼 활성화 조건: 시세 수집 완료 + 날짜 선택
const canStartPaperTrading = computed(() => {
  return collectionStatus.value === 'completed' && !!selectedDate.value
})

// Types for collect API response
type CollectResult = {
  market: string
  status: 'ok' | 'error'
  batchPath?: string
  streamPath?: string
  error?: string
}

type CollectResponse = {
  date: string
  range: { start: string; end: string }
  results: CollectResult[]
}

// 시세 수집 요청 시작 (비동기 API: POST returns 202 + jobId, then poll GET /v1/collect/{jobId})
const requestMarketData = async () => {
  if (!canRequestCollection.value || collectionStatus.value === 'collecting') return

  // clear previous polling and data
  stopPolling()
  collectedData.value = null
  collectJobId.value = ''
  collectCompleted.value = 0
  collectTotal.value = 0
  errorMessage.value = ''

  collectionStatus.value = 'collecting'

  try {
    const payload = { date: selectedDate.value }
    // remember the requested date immediately (do not clear selectedDate)
    requestedCollectDate.value = selectedDate.value
    // persist immediately
    saveStateToSession()
    const res = await fetch('/v1/collect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })

    // Expecting HTTP 202 with jobId
    if (res.status === 202) {
      const json = await res.json()
      const jobId = json?.jobId
      if (!jobId) {
        throw new Error('백엔드가 jobId를 반환하지 않았습니다.')
      }
      collectJobId.value = jobId
      // start polling after 3s
      scheduleNextPoll(3000)
      return
    }

    // Non-202: treat as error
    let backendMsg = ''
    try {
      const errJson = await res.json()
      backendMsg = errJson?.error || JSON.stringify(errJson)
    } catch (e) {
      backendMsg = res.statusText || `HTTP ${res.status}`
    }
    throw new Error(backendMsg)
  } catch (error) {
    handleCollectionError(error instanceof Error ? error : new Error(String(error)))
  }
}

// Schedule next poll in ms (clears any timer first)
function scheduleNextPoll(delayMs) {
  stopPolling()
  pollTimerId = setTimeout(() => {
    pollOnce()
  }, delayMs)
}

// Stop polling and clear timer
function stopPolling() {
  if (pollTimerId) {
    clearTimeout(pollTimerId)
    pollTimerId = null
  }
  pollInFlight = false
}

// Poll one time for job status; prevents overlapping by checking pollInFlight
async function pollOnce() {
  if (!collectJobId.value) return
  if (pollInFlight) return
  pollInFlight = true
  try {
    const res = await fetch(`/v1/collect/${collectJobId.value}`)

    if (res.status === 404) {
      // If backend no longer knows about this job, clear stored state and show initial screen
      // Per requirements: do not surface an error; just reset stored session state.
      clearSessionStorage()
      resetCollection()
      infoMessage.value = ''
      errorMessage.value = ''
      stopPolling()
      return
    }

    if (!res.ok) {
      let backendMsg = ''
      try {
        const errJson = await res.json()
        backendMsg = errJson?.error || JSON.stringify(errJson)
      } catch (e) {
        backendMsg = res.statusText || `HTTP ${res.status}`
      }
      errorMessage.value = backendMsg
      collectionStatus.value = 'failed'
      stopPolling()
      return
    }

    const data = await res.json()
    const status = data?.status

    // If backend provides a date in the response, prefer it for display; otherwise keep requestedCollectDate
    if (data?.date) {
      requestedCollectDate.value = data.date
    }

    // Update progress counts if backend includes them (keeps backward compatibility)
    collectCompleted.value = data?.completed ?? collectCompleted.value
    collectTotal.value = data?.total ?? collectTotal.value
    if (status === 'IN_PROGRESS') {
      collectionStatus.value = 'collecting'
      // schedule next poll in 3s
      scheduleNextPoll(3000)
      return
    }

    if (status === 'COMPLETED') {
      // store results and stop polling
      collectedData.value = data
      // aggregate counts for backward-compatible UI
      const results = Array.isArray(data.results) ? data.results : []
      const marketCount = results.length
      const successCount = results.filter((r) => r.status === 'ok').length
      const failCount = results.filter((r) => r.status === 'error').length
      collectedData.value._summary = { marketCount, successCount, failCount }
      collectionStatus.value = 'completed'
      stopPolling()
      return
    }

    // Unknown status: stop and show message
    errorMessage.value = `알 수 없는 상태값: ${status}`
    collectionStatus.value = 'failed'
    stopPolling()
  } catch (err) {
    // Network or other error: show and stop
    errorMessage.value = err instanceof Error ? err.message : String(err)
    collectionStatus.value = 'failed'
    stopPolling()
  } finally {
    pollInFlight = false
  }
}

// 수집 완료 처리 (API 응답 데이터를 그대로 저장)
const handleCollectionSuccess = (data: { date?: string; marketCount?: number; successCount?: number; failCount?: number }) => {
  collectedData.value = data

  // 완료 판정: 20개 마켓이 모두 성공했을 때만 completed
  const marketCount = data.marketCount ?? 0
  const successCount = data.successCount ?? 0
  const failCount = data.failCount ?? 0

  if (marketCount === 20 && successCount === 20 && failCount === 0) {
    collectionStatus.value = 'completed'
  } else if (failCount > 0) {
    collectionStatus.value = 'failed'
  } else {
    // 미완료 상태(예: 마켓 수가 20 미만이거나 기타 불명확한 경우)는 실패로 처리
    collectionStatus.value = 'failed'
  }
}

// 수집 실패 처리
const handleCollectionError = (error: Error | any) => {
  console.error('시세 수집 실패:', error)
  errorMessage.value = error instanceof Error ? error.message : String(error)
  collectionStatus.value = 'failed'
}

// 시세 수집 상태를 idle로 초기화
const resetCollection = () => {
  collectionStatus.value = 'idle'
  collectedData.value = null
  collectCompleted.value = 0
  collectTotal.value = 0
}

// 수집 날짜가 변경되면 기존 시세 수집 상태(수집 데이터/상태 배지/페이퍼 트레이딩 버튼)를 초기화
watch(selectedDate, () => {
  resetCollection()
})

// ensure timers are cleaned up on unmount
onBeforeUnmount(() => {
  stopPolling()
})

// 페이퍼 트레이딩 실행 상태 (백엔드 API/응답 규격 미확정 — 프론트 상태만 준비)
const executionStatus = ref('idle') // 'idle' | 'running' | 'success' | 'error' | 'stopped'
// 백엔드 응답 타입 확정 전까지는 unknown으로 보관 (TODO: 응답 규격 확정 후 구체 타입 지정)
const paperTradingResult = ref<unknown>(null)
const executionError = ref('')

// 페이퍼 트레이딩 실행 상태를 idle로 초기화
const resetExecution = () => {
  executionStatus.value = 'idle'
  paperTradingResult.value = null
  executionError.value = ''
  previousRunId.value = ''
  awaitingNewRun.value = false
  runStartedAt.value = null
  runEndedAt.value = null
  stopRequested.value = false
}

// ---- Paper trading: integrate with POST /order-api/v1/jobs, GET /order-api/v1/sessions/last-run,
// and GET /v1/orders/summary (recorder). Respect dev proxy; if recorder isn't reachable
// at runtime, show an informative message rather than using an arbitrary path.

let execPollTimerId: any = null
let execPollInFlight = false
let startRequestInFlight = false
const storedRunId = ref('')
// store the runId observed BEFORE issuing a new start request
const previousRunId = ref('')
// whether we're currently waiting for a newly-queued run to appear
const awaitingNewRun = ref(false)
// execution run timestamps (ISO strings)
const runStartedAt = ref<string | null>(null)
const runEndedAt = ref<string | null>(null)

// 유저가 중지(Stop)를 요청했는지 여부 — 폴링이나 새로고침으로 보존되어야 함
const stopRequested = ref(false)
// stop 요청이 네트워크로 전송 중인지(중복 클릭 방지)
const stopRequestInFlight = ref(false)

const formatRFC3339ToKST = (iso: string | null) => {
  if (!iso) return '-'
  try {
    const t = new Date(iso)
    const pad = (n: number) => String(n).padStart(2, '0')
    const year = t.getUTCFullYear()
    const month = pad(t.getUTCMonth() + 1)
    const day = pad(t.getUTCDate())
    const kst = new Date(t.getTime() + 9 * 60 * 60 * 1000)
    const hh = pad(kst.getUTCHours())
    const mm = pad(kst.getUTCMinutes())
    const ss = pad(kst.getUTCSeconds())
    return `${year}-${month}-${day} ${hh}:${mm}:${ss} KST`
  } catch (e) {
    return iso
  }
}

const computeElapsed = (startIso: string | null, endIso?: string | null) => {
  if (!startIso) return '--'
  try {
    const start = new Date(startIso).getTime()
    const end = endIso ? new Date(endIso).getTime() : Date.now()
    const diff = Math.max(0, end - start)
    const s = Math.floor(diff / 1000)
    const hh = String(Math.floor(s / 3600)).padStart(2, '0')
    const mm = String(Math.floor((s % 3600) / 60)).padStart(2, '0')
    const ss = String(s % 60).padStart(2, '0')
    return `${hh}:${mm}:${ss}`
  } catch (e) {
    return '--'
  }
}

function stopExecutionPolling() {
  if (execPollTimerId) {
    clearTimeout(execPollTimerId)
    execPollTimerId = null
  }
  execPollInFlight = false
  startRequestInFlight = false
}

const fetchOrderSummary = async (startedAt: string, endedAt?: string | null) => {
  try {
    const params = new URLSearchParams()
    params.set('mode', 'PAPER_TRADING')
    params.set('from', startedAt)
    if (endedAt) params.set('to', endedAt)
    // Use recorder dev proxy path when available
    const url = `/recorder-api/v1/orders/summary?${params.toString()}`
    const res = await fetch(url)
    if (res.status === 404) {
      return { accepted: 0, filled: 0, unfilled: 0, note: '실행 이력 없음 또는 recorder 미구성' }
    }
    if (!res.ok) return null
    return await res.json()
  } catch (e) {
    return null
  }
}

// 중지 요청: POST /order-api/v1/sessions/{runId}/stop
const stopPaperTrading = async () => {
  if (!storedRunId.value) return
  if (stopRequestInFlight.value) return
  stopRequestInFlight.value = true
  try {
    const res = await fetch(`/order-api/v1/sessions/${storedRunId.value}/stop`, { method: 'POST' })
    if (res.status === 204) {
      stopRequested.value = true
      infoMessage.value = '중지 요청을 보냈습니다. 반영까지 최대 10초 정도 걸릴 수 있습니다.'
      // Do not change executionStatus here; let pollLastRun observe the eventual STOPPED status
      saveStateToSession()
      return
    }
    if (res.status === 404) {
      // NO_ACTIVE_RUN — 이미 종료된 실행
      infoMessage.value = '이미 종료된 실행입니다.'
      // force one immediate poll to refresh UI
      try { await pollLastRun() } catch (e) {}
      return
    }
    // other errors
    let body = null
    try { body = await res.json() } catch (e) {}
    const msg = body?.message || body?.error || `HTTP ${res.status}`
    errorMessage.value = `중지 요청 실패: ${msg}`
  } catch (e) {
    errorMessage.value = e instanceof Error ? e.message : String(e)
  } finally {
    stopRequestInFlight.value = false
    saveStateToSession()
  }
}

const pollLastRun = async () => {
  if (execPollInFlight) return
  execPollInFlight = true
  try {
    const res = await fetch('/order-api/v1/sessions/last-run')
    if (res.status === 404) {
      // If backend no longer knows about any run, and we're awaiting a new run,
      // keep polling. This covers the case where the system never had a trader run
      // before (previousRunId may be empty) but we've just queued one.
      if (awaitingNewRun.value) {
        execPollTimerId = setTimeout(pollLastRun, 3000)
        execPollInFlight = false
        return
      }
      executionStatus.value = 'idle'
      paperTradingResult.value = null
      executionError.value = ''
      infoMessage.value = '실행 이력 없음'
      storedRunId.value = ''
      saveStateToSession()
      stopExecutionPolling()
      return
    }
    if (!res.ok) {
      const err = await res.json().catch(() => ({}))
      executionStatus.value = 'error'
      executionError.value = err?.message || `세션 조회 실패: HTTP ${res.status}`
      stopExecutionPolling()
      return
    }
    const data = await res.json()
    // If we have a previousRunId saved (observed before POST), and the backend
    // still reports the same runId, then this response reflects the old run record
    // rather than the new queued run — keep polling until runId changes.
    const currentRunId = data.runId || ''
    if (awaitingNewRun.value && previousRunId.value && currentRunId && currentRunId === previousRunId.value) {
      // still seeing the previous run; wait and poll again
      execPollTimerId = setTimeout(pollLastRun, 3000)
      execPollInFlight = false
      return
    }

    // We've observed a runId different from the previous one (or there was none).
    // We're no longer awaiting the new run.
    awaitingNewRun.value = false
    saveStateToSession()

    // Now we have either a new runId or no previousRunId to compare; proceed with owner check
    if (data.owner !== 'trader') {
      executionStatus.value = 'error'
      executionError.value = '실행 소유자가 trader가 아니므로 결과를 표시하지 않습니다.'
      // clear marker since this is a terminal state
      previousRunId.value = ''
      saveStateToSession()
      stopExecutionPolling()
      return
    }
    storedRunId.value = currentRunId
    saveStateToSession()
    const status = data.status
    const startedAt = data.startedAt ?? null
    const endedAt = data.endedAt ?? null
    runStartedAt.value = startedAt
    runEndedAt.value = endedAt
    if (status === 'IN_PROGRESS') {
      executionStatus.value = 'running'
      infoMessage.value = data.message || ''
      const summary = await fetchOrderSummary(startedAt)
      if (summary === null) {
        infoMessage.value = '주문 집계 조회 실패: recorder 경로가 설정되어 있는지 확인하세요.'
      } else {
        paperTradingResult.value = summary
      }
      execPollTimerId = setTimeout(pollLastRun, 3000)
      return
    }
    if (status === 'COMPLETED' || status === 'FAILED' || status === 'STOPPED') {
      if (status === 'STOPPED') {
        executionStatus.value = 'stopped'
      } else {
        executionStatus.value = status === 'COMPLETED' ? 'success' : 'error'
      }
      infoMessage.value = data.message || ''
      const summary = await fetchOrderSummary(startedAt, endedAt)
      if (summary === null) {
        infoMessage.value = '최종 주문 집계 조회 실패: recorder 경로가 설정되어 있는지 확인하세요.'
      } else {
        paperTradingResult.value = summary
      }
      // Clear the previousRunId marker now that we've observed a terminal run
      previousRunId.value = ''
      awaitingNewRun.value = false
      // If we saw STOPPED, clear our local stopRequested flag
      stopRequested.value = false
      saveStateToSession()
      stopExecutionPolling()
      return
    }
    executionStatus.value = 'error'
    executionError.value = `알 수 없는 세션 상태: ${status}`
    stopExecutionPolling()
  } finally {
    execPollInFlight = false
  }
}

const startPaperTrading = async () => {
  if (!canStartPaperTrading.value) return
  if (executionStatus.value === 'running' || startRequestInFlight) return
  startRequestInFlight = true
  executionStatus.value = 'running'
  executionError.value = ''
  paperTradingResult.value = null
  infoMessage.value = ''
  try {
    // Record current last-run's runId before sending a new start request.
    // This prevents misinterpreting a still-stale last-run record as the new run.
    try {
      const pre = await fetch('/order-api/v1/sessions/last-run')
      if (pre.status === 404) {
        previousRunId.value = ''
      } else if (pre.ok) {
        const preData = await pre.json()
        previousRunId.value = preData.runId || ''
      } else {
        previousRunId.value = ''
      }
    } catch (e) {
      // network or other error; clear so we don't block polling forever
      previousRunId.value = ''
    }
    // We're now awaiting the appearance of a newly-queued run (regardless of previousRunId value)
    awaitingNewRun.value = true
    saveStateToSession()

    const payload = { jobType: 'ai-trader', date: selectedDate.value, speed: Number(speed.value) }
    const res = await fetch('/order-api/v1/jobs', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
    })
    if (res.status === 202) {
      infoMessage.value = '실행 요청이 큐에 등록되었습니다. 상태를 조회합니다.'
      saveStateToSession()
      stopExecutionPolling()
      await pollLastRun()
      // Note: previousRunId will be cleared by pollLastRun once it sees a new runId and the run completes.
      return
    }
    let body = null
    try { body = await res.json() } catch (e) {}
    const msg = body?.message || body?.error || `HTTP ${res.status}`
    executionStatus.value = 'error'
    executionError.value = `시작 요청 실패: ${msg}`
  } catch (e) {
    executionStatus.value = 'error'
    executionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    startRequestInFlight = false
    saveStateToSession()
  }
}

const reset = () => {
  scenarioName.value = defaultScenarioName
  selectedDate.value = ''
  requestedCollectDate.value = ''
  // totalOrders/generationTime removed
  infoMessage.value = ''
  errorMessage.value = ''
  resetCollection()
  resetExecution()
}

// When user clicks reset, also clear persisted session data
const userReset = () => {
  reset()
  clearSessionStorage()
}

// Save state on relevant changes (debounced by browser automatically)
watch(
  [
    scenarioName,
    selectedDate,
    speed,
    collectJobId,
    collectionStatus,
    collectedData,
    collectCompleted,
    collectTotal,
    executionStatus,
    paperTradingResult,
    executionError,
    requestedCollectDate,
    stopRequested,
    stopRequestInFlight,
  ],
  () => {
    if (restoringFromStorage) return
    saveStateToSession()
  },
  { deep: true }
)

// Restore persisted state when component mounts
onMounted(async () => {
  restoringFromStorage = true
  try {
    const stored = loadStateFromSession()
    if (stored) {
      // Restore UI fields
      scenarioName.value = stored.scenarioName ?? scenarioName.value
      selectedDate.value = stored.selectedDate ?? selectedDate.value
      // removed totalOrders/generationTime from restore
      speed.value = stored.speed ?? speed.value

      // Execution state
      executionStatus.value = stored.executionStatus ?? executionStatus.value
      paperTradingResult.value = stored.paperTradingResult ?? paperTradingResult.value
      runStartedAt.value = stored.runStartedAt ?? runStartedAt.value
      runEndedAt.value = stored.runEndedAt ?? runEndedAt.value
      executionError.value = stored.executionError ?? executionError.value
      previousRunId.value = stored.previousRunId ?? previousRunId.value
      awaitingNewRun.value = stored.awaitingNewRun ?? awaitingNewRun.value
      // stopRequested persistence
      stopRequested.value = stored.stopRequested ?? stopRequested.value
      stopRequestInFlight.value = stored.stopRequestInFlight ?? stopRequestInFlight.value

      // Collection state
      collectJobId.value = stored.collectJobId ?? collectJobId.value
      collectionStatus.value = stored.collectionStatus ?? collectionStatus.value
      collectedData.value = stored.collectedData ?? collectedData.value

      // Restore requested date and any collected progress counters
      requestedCollectDate.value = stored.requestedCollectDate ?? requestedCollectDate.value
      collectCompleted.value = stored.collectCompleted ?? collectCompleted.value
      collectTotal.value = stored.collectTotal ?? collectTotal.value

      // If collection was in progress, resume polling using existing jobId (do not re-POST)
      if (collectionStatus.value === 'collecting' && collectJobId.value) {
        // Do one immediate poll to get up-to-date status, then schedule further polling via pollOnce()
        await pollOnce()
        // If still in progress (pollOnce scheduled next), nothing else needed; otherwise polling stopped.
      } else if (collectionStatus.value === 'completed' && collectJobId.value) {
        // Display results immediately; try a single GET to refresh current status if possible
        try {
          await pollOnce()
        } catch (e) {
          // ignore errors from this single refresh; if job was removed pollOnce handles clearing storage
        }
      }
    }
  } finally {
    restoringFromStorage = false
    // ensure current state saved (normalize any defaults)
    saveStateToSession()
  }
})

const cleanupUnresolvedOrders = async () => {
  if (cleanupLoading.value) return
  const ok = window.confirm('미종결(접수/부분체결) 주문을 전부 취소 처리합니다. 되돌릴 수 없습니다. 계속할까요?')
  if (!ok) return

  cleanupLoading.value = true
  cleanupMessage.value = ''
  cleanupIsError.value = false
  try {
    const res = await fetch('/order-api/v1/admin/cleanup-unresolved-orders', { method: 'POST' })
    if (!res.ok) {
      let body = null
      try { body = await res.json() } catch (e) {}
      throw new Error(body?.message || `HTTP ${res.status}`)
    }
    const data = await res.json()
    cleanupMessage.value = `${data.total}건 중 ${data.canceled}건 취소 완료`
  } catch (e) {
    cleanupIsError.value = true
    cleanupMessage.value = e instanceof Error ? e.message : String(e)
  } finally {
    cleanupLoading.value = false
  }
}

const resetMatchingEngineBook = async () => {
  if (bookResetLoading.value) return
  const ok = window.confirm('매칭엔진 호가창(Redis 스냅샷 + 각 파드 메모리)에 남은 미체결 주문을 전부 비웁니다. 매칭엔진이 재시작됩니다. 계속할까요?')
  if (!ok) return

  bookResetLoading.value = true
  bookResetMessage.value = ''
  bookResetIsError.value = false
  try {
    const res = await fetch('/order-api/v1/admin/reset-matching-engine-book', { method: 'POST' })
    if (!res.ok) {
      let body = null
      try { body = await res.json() } catch (e) {}
      throw new Error(body?.message || `HTTP ${res.status}`)
    }
    const data = await res.json()
    bookResetMessage.value = `스냅샷 ${data.deletedSnapshots}개 삭제, 매칭엔진 재시작 트리거함`
  } catch (e) {
    bookResetIsError.value = true
    bookResetMessage.value = e instanceof Error ? e.message : String(e)
  } finally {
    bookResetLoading.value = false
  }
}
</script>

<template>
  <div>
    <header class="page-header">
      <h2>페이퍼 트레이딩</h2>
      <p class="subtitle">과거 시세를 기반으로 매수·매도 주문 패턴을 생성하고 기록합니다.</p>
      <hr />
    </header>

    <div class="content-grid">
      <section class="panel left-panel">
        <h3 class="panel-title">페이퍼 트레이딩 설정</h3>
        <p class="panel-sub">과거 시세와 생성 조건을 설정합니다</p>

        <div class="form-field">
          <label>페이퍼 트레이딩 이름</label>
          <input v-model="scenarioName" type="text" />
        </div>

        <div class="form-field">
          <label>대상 마켓</label>
          <div class="readonly-input">업비트 KRW 마켓 20개 전체</div>
        </div>

        <div class="form-field">
          <label>시세 수집 날짜</label>
          <input
            v-model="selectedDate"
            type="date"
            :max="todayDate"
            :disabled="collectionStatus === 'collecting'"
          />
          <p class="date-hint">
            선택한 날짜의 KST 00:00부터 다음 날 KST 00:00까지 20개 마켓의 시세를 수집합니다.
          </p>
        </div>

        <div class="form-field">
          <label>재생 배속 (속도)</label>
          <div class="speed-slider-row">
            <input
              type="range"
              min="1"
              max="100"
              step="1"
              v-model.number="speed"
              class="speed-slider"
            />
            <span class="speed-slider-value">{{ speed }}×</span>
          </div>
        </div>

        <div class="collection-section">
          <div class="collection-header">
            <span class="collection-title">과거 시세 수집</span>
            <span class="collection-status-badge" :class="collectionStatusInfo.className">
              <span class="dot"></span>{{ collectionStatusInfo.label }}
            </span>
          </div>

          <div class="collection-data">
            <div v-if="collectionStatus !== 'idle'" style="margin-bottom:8px">
              <div class="collection-data-row">
                <span class="collection-data-key">수집 대상 날짜</span>
                <span class="collection-data-value">{{ collectionTargetDate || '-' }}</span>
              </div>
              <div class="collection-data-row">
                <span class="collection-data-key">수집 범위</span>
                <span class="collection-data-value">{{ collectionRangeDisplay }}</span>
              </div>
            </div>
            <template v-if="collectionStatus === 'idle'">수집된 시세 데이터 없음</template>
            <template v-else-if="collectionStatus === 'collecting'">
              <div class="progress-info">
                <span>{{ selectedDate }} 시세 수집 중...</span>
                <span class="progress-count">{{ collectCompleted }}/{{ collectTotal || 20 }} 마켓 ({{ collectProgressPercent }}%)</span>
              </div>
              <div class="progress-bar-track">
                <div class="progress-bar-fill" :style="{ width: collectProgressPercent + '%' }"></div>
              </div>
            </template>
            <template v-else-if="collectionStatus === 'failed'">시세 수집 실패: {{ errorMessage || '다시 요청해주세요.' }}</template>
            <template v-else-if="collectionStatus === 'completed'">
              <div class="collection-data-list">
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 날짜</span>
                  <span class="collection-data-value">{{ collectedData?.date ?? '-' }}</span>
                </div>
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 마켓 수</span>
                  <span class="collection-data-value">{{ collectedData?._summary?.marketCount ?? '-' }}</span>
                </div>
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 성공 마켓 수</span>
                  <span class="collection-data-value">{{ collectedData?._summary?.successCount ?? '-' }}</span>
                </div>
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 실패 마켓 수</span>
                  <span class="collection-data-value">{{ collectedData?._summary?.failCount ?? '-' }}</span>
                </div>
              </div>

              <div class="collection-results" style="margin-top:12px">
                <h4 style="margin:0 0 8px 0">마켓별 결과</h4>
                <div v-if="!collectedData?.results || collectedData.results.length === 0">결과가 없습니다.</div>
                <div v-else>
                  <div v-for="(r, idx) in collectedData.results" :key="idx" class="result-row">
                    <div style="display:flex;justify-content:space-between;gap:12px;padding:8px;background:#071826;border-radius:8px;margin-bottom:6px">
                      <div><strong>{{ r.market }}</strong> — <span style="color:#9fb0c2">{{ r.status }}</span></div>
                      <div>
                        <template v-if="r.status === 'ok'">성공</template>
                        <template v-else>오류: {{ r.error || '상세 정보 없음' }}</template>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </template>
          </div>

          <button
            type="button"
            class="btn-primary collection-request-btn"
            :disabled="collectionButtonDisabled"
            @click="requestMarketData"
          >
            {{ collectionButtonLabel }}
          </button>
        </div>

        <!-- 목표 주문 수 / 생성 시간 / 목표 처리량 removed -->

        <div class="actions">
          <button class="btn-primary" :disabled="!canStartPaperTrading" @click="startPaperTrading">
            페이퍼 트레이딩 시작
          </button>
          <button class="btn-dark" @click="userReset">초기화</button>
          <!-- Stop button: visible only when running and we have a storedRunId -->
          <button
            v-if="executionStatus === 'running'"
            :class="['btn-stop']"
            :disabled="!storedRunId || stopRequestInFlight"
            @click="stopPaperTrading"
            style="margin-left:8px"
          >
            {{ stopRequestInFlight ? '중지 요청됨' : '중지' }}
          </button>
        </div>

        <div v-if="errorMessage" class="error-note">{{ errorMessage }}</div>
        <div v-if="infoMessage" class="info-note">{{ infoMessage }}</div>
      </section>

      <aside class="panel right-panel">
        <h3 class="panel-title">페이퍼 트레이딩 결과</h3>
        <p class="panel-sub">페이퍼 트레이딩 실행 상태와 백엔드에서 받은 결과를 표시합니다.</p>

        <div class="result-panel">
          <template v-if="executionStatus === 'idle'">
            <div class="result-title">페이퍼 트레이딩 실행 전</div>
            <div class="result-desc">페이퍼 트레이딩을 시작하면 실행 결과가 표시됩니다.</div>
          </template>

          <template v-else-if="executionStatus === 'running'">
            <div class="result-spinner"></div>
            <div class="result-title">페이퍼 트레이딩 진행 중</div>
            <div class="result-desc">AI 트레이더가 주문을 생성하고 기록하고 있습니다.</div>
            <div v-if="typedPaperTradingResult" class="result-stats">
              <div class="stat">
                <div class="stat-value">{{ typedPaperTradingResult.accepted }}</div>
                <div class="stat-label">접수</div>
              </div>
              <div class="stat">
                <div class="stat-value">{{ typedPaperTradingResult.filled }}</div>
                <div class="stat-label">체결</div>
              </div>
              <div class="stat">
                <div class="stat-value">{{ typedPaperTradingResult.unfilled }}</div>
                <div class="stat-label">미체결</div>
              </div>
            </div>
            <div class="result-meta">
              시작: {{ formatRFC3339ToKST(runStartedAt) }} • 경과: {{ computeElapsed(runStartedAt) }}
            </div>
          </template>

          <template v-else-if="executionStatus === 'error'">
            <div class="result-title result-title-error">페이퍼 트레이딩 실행 실패</div>
            <div class="result-desc">
              {{ executionError || '페이퍼 트레이딩 실행 중 오류가 발생했습니다.' }}
            </div>
          </template>

          <template v-else-if="executionStatus === 'success'">
            <div v-if="typedPaperTradingResult">
              <div class="result-stats">
                <div class="stat">
                  <div class="stat-value">{{ typedPaperTradingResult.accepted }}</div>
                  <div class="stat-label">접수</div>
                </div>
                <div class="stat">
                  <div class="stat-value">{{ typedPaperTradingResult.filled }}</div>
                  <div class="stat-label">체결</div>
                </div>
                <div class="stat">
                  <div class="stat-value">{{ typedPaperTradingResult.unfilled }}</div>
                  <div class="stat-label">미체결</div>
                </div>
              </div>
              <div class="result-meta">
                {{ formatRFC3339ToKST(runStartedAt) }} ~ {{ formatRFC3339ToKST(runEndedAt) }} • 총 소요: {{ computeElapsed(runStartedAt, runEndedAt) }}
              </div>
            </div>
            <div v-else class="result-desc">주문 집계를 불러오지 못했습니다.</div>
          </template>
          <template v-else-if="executionStatus === 'stopped'">
            <div v-if="typedPaperTradingResult">
              <div class="result-title result-title-stopped">페이퍼 트레이딩 중지됨</div>
              <div class="result-stats">
                <div class="stat">
                  <div class="stat-value">{{ typedPaperTradingResult.accepted }}</div>
                  <div class="stat-label">접수</div>
                </div>
                <div class="stat">
                  <div class="stat-value">{{ typedPaperTradingResult.filled }}</div>
                  <div class="stat-label">체결</div>
                </div>
                <div class="stat">
                  <div class="stat-value">{{ typedPaperTradingResult.unfilled }}</div>
                  <div class="stat-label">미체결</div>
                </div>
              </div>
              <div class="result-meta">
                {{ formatRFC3339ToKST(runStartedAt) }} ~ {{ formatRFC3339ToKST(runEndedAt) }} • 총 소요: {{ computeElapsed(runStartedAt, runEndedAt) }}
              </div>
            </div>
            <div v-else class="result-desc">주문 집계를 불러오지 못했습니다.</div>
          </template>
        </div>
      </aside>
    </div>

    <section class="cleanup-section">
      <div class="cleanup-text">
        <h4>미종결 주문 일괄 정리</h4>
        <p>과거 실행들이 남긴 접수/부분체결 상태 주문을 전부 취소합니다. 세션이 끝날 때마다 자동으로 그 실행 몫은 정리되지만, 그 이전에 쌓인 건 이 버튼으로 처리해야 합니다.</p>
      </div>
      <button class="btn-dark" :disabled="cleanupLoading" @click="cleanupUnresolvedOrders">
        {{ cleanupLoading ? '정리 중...' : '일괄 정리' }}
      </button>
      <div v-if="cleanupMessage" :class="['cleanup-message', { error: cleanupIsError }]">{{ cleanupMessage }}</div>
    </section>

    <section class="cleanup-section">
      <div class="cleanup-text">
        <h4>매칭엔진 호가창 잔량 지우기</h4>
        <p>위 "일괄 정리"는 DB(recorder) 기준으로만 정리됩니다 — 매칭엔진이 그 취소를 못 받았거나 저장에 실패하면 매칭엔진 자신의 Redis 스냅샷과 각 파드 메모리에는 미체결 주문이 그대로 남을 수 있습니다. 이 버튼은 그 잔량을 지우고 매칭엔진을 재시작합니다(진행 중인 세션이 있으면 매칭이 잠시 끊깁니다).</p>
      </div>
      <button class="btn-dark" :disabled="bookResetLoading" @click="resetMatchingEngineBook">
        {{ bookResetLoading ? '초기화 중...' : '잔량 지우기' }}
      </button>
      <div v-if="bookResetMessage" :class="['cleanup-message', { error: bookResetIsError }]">{{ bookResetMessage }}</div>
    </section>
  </div>
</template>

<style scoped>
.page-header h2 {
  margin: 0 0 6px 0;
  font-size: 22px;
  color: #ffffff;
}
.subtitle {
  margin: 0 0 14px 0;
  color: #9fb0c2;
}
.page-header hr {
  border: 0;
  height: 1px;
  background: #0f2636;
  margin-bottom: 20px;
}
.content-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 24px;
}
.panel {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
}
.panel-title {
  font-size: 16px;
  margin: 0 0 6px 0;
}
.panel-sub {
  color: #9fb0c2;
  margin: 0 0 16px 0;
}
.form-field {
  margin-bottom: 12px;
}
.form-field label {
  display: block;
  font-size: 12px;
  color: #9fb0c2;
  margin-bottom: 8px;
}
.form-field input,
.form-field select {
  width: 100%;
  padding: 12px 14px;
  background: #072037;

.speed-slider-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.speed-slider {
  flex: 1;
  -webkit-appearance: none;
  appearance: none;
  height: 6px;
  border-radius: 999px;
  background: #0f2636;
  outline: none;
}
.speed-slider::-webkit-slider-thumb {
  -webkit-appearance: none;
  appearance: none;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: #3f86ff;
  cursor: pointer;
  border: 2px solid #e6eef8;
}
.speed-slider::-moz-range-thumb {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: #3f86ff;
  cursor: pointer;
  border: 2px solid #e6eef8;
  border: none;
}
.speed-slider-value {
  min-width: 48px;
  text-align: right;
  font-weight: 700;
  color: #7fb2ff;
  font-variant-numeric: tabular-nums;
}
  border: 1px solid #163247;
  color: #e6eef8;
  border-radius: 8px;
  outline: none;
}
.two-cols {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.date-hint {
  margin: 8px 0 0 0;
  color: #9fb0c2;
  font-size: 12px;
}
.readonly-input {
  padding: 12px 14px;
  background: #072037;
  border: 1px solid #163247;
  color: #cfe6ff;
  border-radius: 8px;
}
.actions {
  display: flex;
  gap: 12px;
  margin-top: 16px;
}
.btn-primary {
  flex: 1.4;
  background: #3f86ff;
  color: #fff;
  border: 0;
  padding: 12px 18px;
  border-radius: 10px;
  cursor: pointer;
}
.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-dark {
  flex: 0.8;
  background: #18324a;
  color: #e6eef8;
  border: 0;
  padding: 12px 18px;
  border-radius: 10px;
  cursor: pointer;
}
/* Stop button */
.btn-stop {
  background: #c97a2e;
  color: white;
  border: none;
  padding: 10px 14px;
  border-radius: 8px;
  cursor: pointer;
}
.btn-stop[disabled] {
  opacity: 0.6;
  cursor: not-allowed;
}
.result-title-stopped {
  color: #ff7a59;
  font-weight: 600;
}
.result-panel {
  min-height: 260px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  text-align: center;
  gap: 8px;
  background: #071a28;
  border: 1px solid #122a3d;
  border-radius: 8px;
  padding: 32px 16px;
}
.result-title {
  font-weight: 700;
  font-size: 15px;
  color: #d7e8fb;
}
.result-title-error {
  color: #ff6b6b;
}
.result-desc {
  color: #9fb0c2;
  font-size: 13px;
}
.result-spinner {
  width: 28px;
  height: 28px;
  margin-bottom: 4px;
  border: 3px solid #163247;
  border-top-color: #3f86ff;
  border-radius: 50%;
  animation: result-spin 0.8s linear infinite;
}
@keyframes result-spin {
  to {
    transform: rotate(360deg);
  }
}

.error-note {
  margin-top: 10px;
  color: #ff6b6b;
  font-weight: 700;
}

.info-note {
  margin-top: 8px;
  color: #cfe6ff;
  background: #072037;
  padding: 10px;
  border-radius: 8px;
  border: 1px solid #163247;
}

.collection-section {
  margin: 16px 0;
  background: #081826;
  border: 1px solid #122a3d;
  border-radius: 10px;
  padding: 14px;
}
.collection-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.collection-title {
  font-weight: 700;
  color: #d7e8fb;
  font-size: 14px;
}
.collection-status-badge {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-radius: 20px;
  font-weight: 700;
  font-size: 12px;
}
.collection-status-badge .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.collection-status-badge.status-idle {
  background: rgba(159, 176, 194, 0.12);
  color: #9fb0c2;
}
.collection-status-badge.status-idle .dot {
  background: #9fb0c2;
}
.collection-status-badge.status-collecting {
  background: rgba(63, 134, 255, 0.12);
  color: #7fb2ff;
}
.collection-status-badge.status-collecting .dot {
  background: #3f86ff;
}
.collection-status-badge.status-completed {
  background: rgba(46, 211, 154, 0.12);
  color: #2ed39a;
}
.collection-status-badge.status-completed .dot {
  background: #2ed39a;
}
.collection-status-badge.status-failed {
  background: rgba(255, 107, 107, 0.12);
  color: #ff6b6b;
}
.collection-status-badge.status-failed .dot {
  background: #ff6b6b;
}
.collection-data {
  color: #9fb0c2;
  font-size: 13px;
}
.collection-data-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.collection-data-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  color: #cfe6ff;
}
.collection-data-key {
  color: #9fb0c2;
}
.collection-request-btn {
  width: 100%;
  margin-top: 12px;
}
.progress-info {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.progress-count {
  color: #7fb2ff;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.progress-bar-track {
  width: 100%;
  height: 8px;
  background: #0f2636;
  border-radius: 999px;
  overflow: hidden;
}
.progress-bar-fill {
  height: 100%;
  background: #3f86ff;
  border-radius: 999px;
  transition: width 0.4s ease;
}

/* Result stats */
.result-stats {
  display: flex;
  justify-content: center;
  gap: 24px;
  margin: 12px 0;
}
.stat {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}
.stat-value {
  font-size: 22px;
  font-weight: 700;
  color: #d7e8fb;
  font-variant-numeric: tabular-nums;
}
.stat-label {
  font-size: 12px;
  color: #9fb0c2;
}
.result-meta {
  font-size: 12px;
  color: #9fb0c2;
  margin-top: 8px;
}

@media (max-width: 900px) {
  .content-grid {
    grid-template-columns: 1fr;
  }
}

.cleanup-section {
  display: flex;
  margin-top: 24px;
  padding: 16px 20px;
  align-items: center;
  gap: 16px;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
}
.cleanup-text {
  flex: 1;
}
.cleanup-text h4 {
  margin: 0 0 4px 0;
  font-size: 14px;
  color: #d7e8fb;
}
.cleanup-text p {
  margin: 0;
  color: #9fb0c2;
  font-size: 12px;
}
.cleanup-message {
  color: #2ed39a;
  font-size: 12px;
  font-weight: 700;
  white-space: nowrap;
}
.cleanup-message.error {
  color: #ff6b6b;
}

@media (max-width: 900px) {
  .cleanup-section {
    flex-direction: column;
    align-items: stretch;
  }
}
</style>
