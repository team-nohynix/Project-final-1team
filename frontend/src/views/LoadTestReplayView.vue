<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, computed, watch } from 'vue'

// user-selectable run date (KST day)
const selectedDate = ref('') // YYYY-MM-DD

// target speed multiplier (numeric)
const speed = ref(60)

// shardCount replaces pod selection: number of replay shards (1..20)
const shardCount = ref(1)

// UI / state
const precheckMessage = ref('')
const startMessage = ref('')
const errorMessage = ref('')

// replay run state
const isStarting = ref(false)
const isPolling = ref(false)
const pollTimer = ref<number | null>(null)

// store last-run seen before starting so we wait for a new runId
const previousRunId = ref<string | null>(null)

// current run info from GET /order-api/v1/sessions/last-run
const runInfo = ref<{ runId: string; owner: string; status: string; startedAt?: string; endedAt?: string; message?: string } | null>(null)

// recorder summary
const summary = ref<{ accepted: number; filled: number; unfilled: number } | null>(null)

// replay preview from backend
const preview = ref<null | { date: string; totalOrders: number; marketsWithRecords: number; marketsTotal: number; maxEventSpanSeconds: number }>(null)
const previewError = ref<string | null>(null)
const previewLoading = ref(false)

function formatSecondsToHMS(secTotal: number) {
  if (!Number.isFinite(secTotal) || secTotal <= 0) return '--'
  const s = Math.floor(secTotal)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sRem = s % 60
  const parts = []
  if (h) parts.push(`${h}h`)
  if (m) parts.push(`${m}m`)
  parts.push(`${sRem}s`)
  return parts.join(' ')
}

const estimatedDurationDisplay = computed(() => {
  if (!preview.value || !preview.value.maxEventSpanSeconds) return '--'
  const speedVal = Number(speed.value) || 1
  const estSec = preview.value.maxEventSpanSeconds / speedVal
  return formatSecondsToHMS(estSec)
})

async function fetchReplayPreview(date: string) {
  previewError.value = null
  previewLoading.value = true
  preview.value = null
  if (!date) {
    previewLoading.value = false
    return
  }
  try {
    const res = await fetch(`/order-api/v1/jobs/replay-preview?date=${encodeURIComponent(date)}`)
    if (!res.ok) {
      const txt = await res.text()
      throw new Error(`프리뷰 조회 실패: ${res.status} ${txt}`)
    }
    const data = await res.json()
    // expected shape: { date, totalOrders, marketsWithRecords, marketsTotal, maxEventSpanSeconds }
    preview.value = data
  } catch (e: any) {
    previewError.value = e?.message || String(e)
  } finally {
    previewLoading.value = false
  }
}

// watch selectedDate changes
watch(selectedDate, (newD, oldD) => {
  preview.value = null
  previewError.value = null
  if (!newD) return
  fetchReplayPreview(newD)
})

// percentages relative to accepted (accepted as 100%)
const acceptedCount = computed(() => Number(summary.value?.accepted ?? 0))
const filledPercent = computed(() => {
  const a = acceptedCount.value
  if (!a) return 0
  return Math.round((Number(summary.value?.filled ?? 0) / a) * 100)
})
const unfilledPercent = computed(() => {
  const a = acceptedCount.value
  if (!a) return 0
  return Math.round((Number(summary.value?.unfilled ?? 0) / a) * 100)
})

// sessionStorage keys (separate from paper trading)
const SS_PREFIX = 'replay_' // keep distinct
const SS_KEYS = {
  selectedDate: SS_PREFIX + 'selectedDate',
  speed: SS_PREFIX + 'speed',
  shardCount: SS_PREFIX + 'shardCount',
  runInfo: SS_PREFIX + 'runInfo',
}

const validate = () => {
  errorMessage.value = ''
  if (!selectedDate.value) return '재생할 날짜를 선택해주세요'
  const sc = Number(shardCount.value)
  if (!sc || Number.isNaN(sc) || sc < 1 || sc > 20) return '샤드 수는 1~20 사이여야 합니다'
  return ''
}

// lightweight precheck used by the UI
function onPrecheck() {
  const err = validate()
  if (err) {
    errorMessage.value = err
    precheckMessage.value = ''
    return
  }
  errorMessage.value = ''
  precheckMessage.value = '사전 점검 준비 완료'
}

// UI handler that starts the replay flow
function onStart() {
  startReplay()
}

function saveToSession() {
  try {
    sessionStorage.setItem(SS_KEYS.selectedDate, selectedDate.value)
    sessionStorage.setItem(SS_KEYS.speed, String(speed.value))
    sessionStorage.setItem(SS_KEYS.shardCount, String(shardCount.value))
    sessionStorage.setItem(SS_KEYS.runInfo, JSON.stringify(runInfo.value || null))
  } catch (e) {
    // ignore storage errors
  }
}

function loadFromSession() {
  try {
    const sd = sessionStorage.getItem(SS_KEYS.selectedDate)
    if (sd) selectedDate.value = sd
    const sp = sessionStorage.getItem(SS_KEYS.speed)
    if (sp) speed.value = Number(sp)
    const sc = sessionStorage.getItem(SS_KEYS.shardCount)
    if (sc) shardCount.value = Number(sc)
    const ri = sessionStorage.getItem(SS_KEYS.runInfo)
    if (ri) {
      const parsed = JSON.parse(ri)
      if (parsed) runInfo.value = parsed
    }
  } catch (e) {
    // ignore
  }
}

async function fetchLastRun() {
  try {
    const res = await fetch('/order-api/v1/sessions/last-run')
    if (res.status === 404) {
      return { found: false }
    }
    if (!res.ok) {
      const text = await res.text()
      throw new Error(`상태 조회 실패: ${res.status} ${text}`)
    }
    const data = await res.json()
    return { found: true, data }
  } catch (e: any) {
    throw e
  }
}

async function fetchRecorderSummary(from: string, to?: string) {
  try {
    let url = `/recorder-api/v1/orders/summary?mode=REPLAY&from=${encodeURIComponent(from)}`
    if (to) url += `&to=${encodeURIComponent(to)}`
    const res = await fetch(url)
    if (!res.ok) throw new Error(`집계 조회 실패: ${res.status}`)
    const data = await res.json()
    summary.value = data
  } catch (e: any) {
    errorMessage.value = e.message || String(e)
  }
}

// convert RFC3339 to KST display
function toKST(rfc: string | undefined) {
  if (!rfc) return ''
  const d = new Date(rfc)
  // KST = UTC+9
  const opts: Intl.DateTimeFormatOptions = { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }
  // use toLocaleString with 'ko-KR' and timezone offset by constructing with locale and options
  try {
    return d.toLocaleString('ko-KR', {...opts, timeZone: 'Asia/Seoul'})
  } catch {
    // fallback manual offset
    const ts = d.getTime() + 9 * 3600 * 1000
    const kd = new Date(ts)
    return kd.toISOString().replace('T', ' ').replace('Z', '')
  }
}

function computeDuration(start?: string, end?: string) {
  if (!start) return ''
  const s = new Date(start).getTime()
  const e = end ? new Date(end).getTime() : Date.now()
  const diff = e - s
  if (diff < 0) return ''
  const sec = Math.floor(diff / 1000)
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const sRem = sec % 60
  return `${h}h ${m}m ${sRem}s`
}

async function startReplay() {
  errorMessage.value = ''
  precheckMessage.value = ''
  startMessage.value = ''
  const err = validate()
  if (err) {
    errorMessage.value = err
    return
  }

  // prevent duplicate
  if (isStarting.value) return
  isStarting.value = true

  try {
    // 1) pre-fetch last-run and remember runId before starting
    let existingRunId: string | null = null
    try {
      const res = await fetchLastRun()
      if (res.found) {
        const d = res.data
        existingRunId = d.runId || null
      }
    } catch (e: any) {
      // treat last-run errors as non-fatal but surface
      errorMessage.value = '시작 전 마지막 실행 조회 오류: ' + (e.message || String(e))
    }
    previousRunId.value = existingRunId

    // 2) POST start job
    const body = { jobType: 'replay', date: selectedDate.value, speed: Number(speed.value), shardCount: Number(shardCount.value) }
    const res = await fetch('/order-api/v1/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    if (res.status !== 202) {
      const txt = await res.text()
      throw new Error(`시작 요청 실패: ${res.status} ${txt}`)
    }
    startMessage.value = '재생 작업 요청이 큐에 들어갔습니다. 새 실행을 대기합니다.'

    // 3) start polling last-run every 3s
    if (!isPolling.value) {
      isPolling.value = true
      pollTimer.value = window.setInterval(pollLastRun, 3000)
      // immediate first poll
      pollLastRun()
    }
  } catch (e: any) {
    errorMessage.value = e.message || String(e)
  } finally {
    isStarting.value = false
    saveToSession()
  }
}

async function pollLastRun() {
  try {
    const res = await fetchLastRun()
    if (!res.found) {
      // no run yet
      runInfo.value = null
      saveToSession()
      return
    }
    const d = res.data
    // only accept runs with owner === 'replayengine' (use backend's owner contract)
    if (d.owner !== 'replayengine') {
      // ignore runs owned by others
      return
    }

    // If we had a previous runId saved before starting, wait until a new runId appears
    if (previousRunId.value && d.runId === previousRunId.value) {
      // still the old run, keep waiting
      return
    }

    // we have a new run (or there was none before)
    runInfo.value = { runId: d.runId, owner: d.owner, status: d.status, startedAt: d.startedAt, endedAt: d.endedAt, message: d.message }
    saveToSession()

    // when startedAt present, fetch recorder summary
    if (runInfo.value.startedAt) {
      await fetchRecorderSummary(runInfo.value.startedAt, runInfo.value.endedAt)
    }

    // update UI message
    if (runInfo.value.status === 'IN_PROGRESS') {
      startMessage.value = '재생 중'
    } else if (runInfo.value.status === 'COMPLETED') {
      startMessage.value = '재생 완료'
      // final fetch and stop polling
      if (runInfo.value.startedAt) await fetchRecorderSummary(runInfo.value.startedAt, runInfo.value.endedAt)
      stopPolling()
    } else if (runInfo.value.status === 'FAILED') {
      startMessage.value = '재생 실패'
      if (runInfo.value.startedAt) await fetchRecorderSummary(runInfo.value.startedAt, runInfo.value.endedAt)
      stopPolling()
    }
  } catch (e: any) {
    errorMessage.value = e.message || String(e)
  }
}

function stopPolling() {
  if (pollTimer.value !== null) {
    clearInterval(pollTimer.value)
    pollTimer.value = null
  }
  isPolling.value = false
}

onMounted(() => {
  loadFromSession()
  // if we restored an IN_PROGRESS run, resume polling to show live results
  if (runInfo.value && runInfo.value.status === 'IN_PROGRESS') {
    if (!isPolling.value) {
      isPolling.value = true
      pollTimer.value = window.setInterval(pollLastRun, 3000)
      pollLastRun()
    }
  }
})

onBeforeUnmount(() => {
  stopPolling()
})
</script>

<template>
  <div>
    <header class="page-header">
      <h2>부하 테스트 제어</h2>
      <p class="subtitle">AI 트레이더 주문 기록과 동일 패턴 재생 설정</p>
      <hr />
    </header>
    

    <div class="content-grid">
      <section class="panel left-panel">
        <h3 class="panel-title">주문 재생 설정</h3>
        <p class="panel-sub">성능 비교를 위한 결정적 주문 재생</p>

        <div class="form-field">
          <label>재생 날짜</label>
          <input v-model="selectedDate" type="date" />
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

        <div class="form-field">
          <label>샤드 수 (shardCount)</label>
          <input v-model.number="shardCount" type="number" min="1" max="20" />
          <p class="date-hint">샤드 수는 1~20 사이의 정수입니다. 백엔드 연동 시 shardCount로 전달됩니다.</p>
        </div>

        <div class="actions">
          <button class="btn-primary" :disabled="isStarting || isPolling" @click="onStart">재생 시작</button>
          <button class="btn-dark" :disabled="isStarting" @click="onPrecheck">사전 점검</button>
        </div>

        <div class="messages">
          <p v-if="errorMessage" class="error">{{ errorMessage }}</p>
          <p v-if="precheckMessage" class="success">{{ precheckMessage }}</p>
          <p v-if="startMessage" class="info">{{ startMessage }}</p>
        </div>
      </section>

      <aside class="panel right-panel">
        <h3 class="panel-title">부하 시나리오 미리보기</h3>
        <p class="panel-sub">예상 부하 분포와 검증 기준</p>

        <div class="bar-chart placeholder">
          <div v-if="previewLoading" class="empty-center">
            <strong>프리뷰 불러오는 중...</strong>
          </div>

          <div v-else-if="previewError" class="empty-center">
            <strong>프리뷰 로드 실패</strong>
            <div class="empty-sub">{{ previewError }}</div>
          </div>

          <div v-else-if="preview && preview.totalOrders === 0" class="empty-center">
            <strong>이 날짜엔 기록된 주문이 없습니다.</strong>
            <div class="empty-sub">먼저 페이퍼 트레이딩을 실행하세요.</div>
          </div>

          <div v-else-if="preview" class="preview-body">
            <div class="preview-line">
              <div class="preview-main">총 {{ preview.totalOrders.toLocaleString() }}건 재생 예정 · {{ preview.marketsWithRecords }}/{{ preview.marketsTotal }}개 마켓</div>
            </div>
            <div class="preview-line" style="margin-top:8px">
              <div class="preview-label">예상 소요 시간 (배속 {{ speed }}×):</div>
              <div class="preview-value">{{ estimatedDurationDisplay }}</div>
            </div>

            <div class="preview-checks" style="margin-top:12px">
              <div class="check-item">검증 기준: 목표 처리량 10,000건/초 · 목표 체결률 90% · 허용 거부율 5%</div>
            </div>
          </div>

          <div v-else class="empty-center">
            <strong>시나리오 미리보기: 데이터 연동 예정</strong>
            <div class="empty-sub">날짜를 선택하면 자동으로 프리뷰를 조회합니다.</div>
          </div>
        </div>

          <div class="status-box">
          <div class="status-left">
            <span class="status-dot"></span>
            <span>{{ runInfo ? runInfo.status : '상태 확인 전' }}</span>
          </div>
          <div class="status-right">
            <div v-if="runInfo">
              <div>{{ runInfo.owner }} • {{ runInfo.runId }}</div>
              <div v-if="runInfo.startedAt">시작: {{ toKST(runInfo.startedAt) }}</div>
              <div v-if="runInfo.endedAt">종료: {{ toKST(runInfo.endedAt) }} (소요: {{ computeDuration(runInfo.startedAt, runInfo.endedAt) }})</div>
            </div>
            <div v-else>데이터 연동 전</div>
          </div>
        </div>
          <!-- Recorder summary: 접수/체결/미체결 -->
          <div class="summary-box">
            <div v-if="!summary">데이터 없음</div>
            <div v-else class="ratios">
              <div class="section-title">요약</div>

              <div class="ratio-row">
                <div class="ratio-label">접수</div>
                <div class="ratio-bar">
                  <div class="ratio-fill" :style="{ width: '100%', background: '#163247' }"></div>
                </div>
                <div class="ratio-value">{{ summary.accepted }}</div>
              </div>

              <div class="ratio-row">
                <div class="ratio-label">체결</div>
                <div class="ratio-bar">
                  <div class="ratio-fill" :style="{ width: filledPercent + '%', background: '#3f86ff' }"></div>
                </div>
                <div class="ratio-value">{{ summary.filled }} ({{ filledPercent }}%)</div>
              </div>

              <div class="ratio-row">
                <div class="ratio-label">미체결</div>
                <div class="ratio-bar">
                  <div class="ratio-fill" :style="{ width: unfilledPercent + '%', background: '#ff6b6b' }"></div>
                </div>
                <div class="ratio-value">{{ summary.unfilled }} ({{ unfilledPercent }}%)</div>
              </div>
            </div>
          </div>
      </aside>
    </div>
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
  border: 1px solid #163247;
  color: #e6eef8;
  border-radius: 8px;
  outline: none;
}

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
.form-field input[type='number']::-webkit-outer-spin-button,
.form-field input[type='number']::-webkit-inner-spin-button {
  -webkit-appearance: none;
  margin: 0;
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
.btn-dark {
  flex: 0.8;
  background: #18324a;
  color: #e6eef8;
  border: 0;
  padding: 12px 18px;
  border-radius: 10px;
  cursor: pointer;
}

.messages {
  margin-top: 12px;
}
.messages .error {
  color: #ff6b6b;
}
.messages .success {
  color: #2ed39a;
}
.messages .info {
  color: #cfe6ff;
}

.right-panel {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
}

.bar-chart .bars {
  display: flex;
  align-items: flex-end;
  gap: 6px;
  height: 140px;
  padding: 12px 6px;
  background: transparent;
}
.bar-chart .bar {
  width: 14px;
  background: #8b5cf6;
  border-radius: 6px 6px 0 0;
}
.bar-chart .bar.teal {
  background: #20c8e8;
}

.section-title {
  margin: 16px 0 8px 0;
  color: #d7e8fb;
}
.ratios {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.ratio-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.ratio-label {
  width: 120px;
  color: #c6d6e6;
}
.ratio-bar {
  flex: 1;
  height: 12px;
  background: #072b45;
  border-radius: 8px;
  overflow: hidden;
}
.ratio-fill {
  height: 100%;
}
.ratio-value {
  width: 48px;
  text-align: right;
  color: #c6d6e6;
}

.status-box {
  margin-top: 18px;
  background: #081826;
  border-radius: 10px;
  padding: 12px 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: #bcd8e9;
}
.summary-box {
  margin-top: 18px;
}
.status-dot {
  width: 10px;
  height: 10px;
  background: #2ed39a;
  border-radius: 50%;
  margin-right: 8px;
}
.status-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

@media (max-width: 900px) {
  .content-grid {
    grid-template-columns: 1fr;
  }
  body {
    min-width: 0;
  }
}
</style>
