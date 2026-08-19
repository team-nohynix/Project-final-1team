<script setup lang="ts">
import { ref, onBeforeUnmount, onMounted } from 'vue'

// input
const traceId = ref('')

// run / summary state (replaces earlier placeholders)
const runInfo = ref<any | null>(null)
const runLoading = ref(false)
// flag when a last-run exists but is not from replayengine
const lastRunNotReplay = ref(false)
const summary = ref<any>({ accepted: null, filled: null, unfilled: null })

// constants and labels
const OWNER_LABEL: Record<string, string> = {
  trader: '페이퍼 트레이딩',
  replayengine: '주문 재생',
}

// UI states: 'idle' | 'loading' | 'success' | 'error'
const uiState = ref<'idle' | 'loading' | 'success' | 'error'>('idle')
const searchMessage = ref('')

// response holder
const traceData = ref<any | null>(null)

// polling handle for last-run
let pollInterval: number | null = null

// Abort and latest-response guard
let currentController: AbortController | null = null
let latestRequestSeq = 0

function formatNumberString(s: string) {
  if (s == null) return '--'
  // keep original precision; add thousands separators to integer part
  const parts = String(s).split('.')
  const intPart = parts[0]
  const decPart = parts[1]
  const withSep = intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  return decPart ? `${withSep}.${decPart}` : withSep
}

function toKST(iso?: string) {
  if (!iso) return '--'
  try {
    const d = new Date(iso)
    // KST = UTC+9
    const kst = new Date(d.getTime() + 9 * 60 * 60 * 1000)
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${kst.getFullYear()}-${pad(kst.getMonth() + 1)}-${pad(kst.getDate())} ${pad(kst.getHours())}:${pad(kst.getMinutes())}:${pad(kst.getSeconds())} KST`
  } catch (e) {
    return iso
  }
}

const STATUS_LABEL: Record<string, string> = {
  ACCEPTED: '접수',
  PARTIALLY_FILLED: '부분 체결',
  FILLED: '체결 완료',
  CANCELED: '취소',
}

const MODE_LABEL: Record<string, string> = {
  PAPER_TRADING: '페이퍼 트레이딩',
  REPLAY: '주문 재생',
}

async function doSearch() {
  searchMessage.value = ''
  traceData.value = null
  if (!traceId.value || !traceId.value.trim()) {
    uiState.value = 'error'
    searchMessage.value = '주문 ID를 입력하세요.'
    return
  }

  // cancel previous
  if (currentController) {
    try { currentController.abort() } catch (_) {}
    currentController = null
  }
  const controller = new AbortController()
  currentController = controller
  const requestSeq = ++latestRequestSeq

  uiState.value = 'loading'
  searchMessage.value = ''

  const url = `/recorder-api/v1/trace/${encodeURIComponent(traceId.value.trim())}`
  try {
    const res = await fetch(url, { signal: controller.signal })

    // ignore if a newer request started
    if (requestSeq !== latestRequestSeq) return

    if (res.status === 404) {
      // try to parse body for error code
      let body: any = null
      try { body = await res.json() } catch (_) { body = null }
      if (body && body.errorCode === 'ORDER_NOT_FOUND') {
        uiState.value = 'error'
        searchMessage.value = '해당 주문을 찾을 수 없습니다.'
        traceData.value = null
        return
      }
      uiState.value = 'error'
      searchMessage.value = '해당 주문을 찾을 수 없습니다.'
      traceData.value = null
      return
    }

    if (!res.ok) {
      let body: any = null
      try { body = await res.json() } catch (_) { body = null }
      uiState.value = 'error'
      if (body && body.message) searchMessage.value = String(body.message)
      else searchMessage.value = `주문 추적 조회에 실패했습니다. (HTTP ${res.status})`
      traceData.value = null
      return
    }

    const data = await res.json()

    if (requestSeq !== latestRequestSeq) return

    // success — assign and format nothing destructive (keep strings)
    traceData.value = data
    uiState.value = 'success'
    searchMessage.value = ''
  } catch (err: any) {
    if (err && err.name === 'AbortError') {
      // aborted — swallow silently
      return
    }
    uiState.value = 'error'
    traceData.value = null
    searchMessage.value = err?.message || '주문 추적 조회에 실패했습니다.'
  } finally {
    // clear controller if it's the same
    if (currentController === controller) currentController = null
  }
}

onBeforeUnmount(() => {
  if (currentController) {
    try { currentController.abort() } catch (_) {}
    currentController = null
  }
  if (pollInterval) {
    try { clearInterval(pollInterval) } catch (_) {}
    pollInterval = null
  }
})

onMounted(() => {
  // fetch most recent run once on mount
  fetchLastRun()
})

function prettyDuration(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '--'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  const parts = []
  if (h) parts.push(`${h}h`)
  if (m) parts.push(`${m}m`)
  parts.push(`${s}s`)
  return parts.join(' ')
}

async function fetchLastRun() {
  runLoading.value = true
  try {
    const res = await fetch('/order-api/v1/sessions/last-run')
    if (res.status === 404) {
      runInfo.value = null
      lastRunNotReplay.value = false
      // stop polling if any
      if (pollInterval) { clearInterval(pollInterval); pollInterval = null }
      return
    }
    if (!res.ok) {
      runInfo.value = null
      lastRunNotReplay.value = false
      return
    }
    const data = await res.json()
    // only treat it as a replay run if owner === 'replayengine'
    if (data && data.owner === 'replayengine') {
      runInfo.value = data
      lastRunNotReplay.value = false

      // if in progress, start polling every 3s
      if (data.status === 'IN_PROGRESS') {
        if (!pollInterval) {
          pollInterval = window.setInterval(async () => {
            try {
              const r = await fetch('/order-api/v1/sessions/last-run')
              if (!r.ok) return
              const d = await r.json()
              // only accept updates when owner is still replayengine
              if (d && d.owner === 'replayengine') {
                runInfo.value = d
                if (d.status !== 'IN_PROGRESS') {
                  if (pollInterval) { clearInterval(pollInterval); pollInterval = null }
                  // once completed, fetch summary for final numbers
                  fetchSummary()
                }
              } else {
                // owner changed or no longer replayengine: treat as no replay history
                runInfo.value = null
                lastRunNotReplay.value = true
                if (pollInterval) { clearInterval(pollInterval); pollInterval = null }
              }
            } catch (_) {}
          }, 3000)
        }
      } else {
        // completed/failed: fetch summary
        fetchSummary()
        if (pollInterval) { clearInterval(pollInterval); pollInterval = null }
      }
    } else {
      // last run exists but is not from replayengine — treat as no recent replay run
      runInfo.value = null
      lastRunNotReplay.value = true
      // stop polling
      if (pollInterval) { clearInterval(pollInterval); pollInterval = null }
    }
  } catch (e) {
    runInfo.value = null
    lastRunNotReplay.value = false
  } finally {
    runLoading.value = false
  }
}

async function fetchSummary() {
  // always query REPLAY mode for this screen; include from/to if available
  const params = new URLSearchParams()
  params.append('mode', 'REPLAY')
  if (runInfo.value && runInfo.value.startedAt) params.append('from', runInfo.value.startedAt)
  if (runInfo.value && runInfo.value.endedAt) params.append('to', runInfo.value.endedAt)
  try {
    const res = await fetch(`/recorder-api/v1/orders/summary?${params.toString()}`)
    if (!res.ok) {
      summary.value = { accepted: null, filled: null, unfilled: null }
      return
    }
    const data = await res.json()
    summary.value = data
  } catch (e) {
    summary.value = { accepted: null, filled: null, unfilled: null }
  }
}
</script>

<template>
  <div class="trt-page">
    <header class="page-header">
      <h2>테스트 결과·추적</h2>
      <p class="subtitle">성능 시험 결과, 주문 단위 구간 추적</p>
      <hr />
    </header>

    <div class="experiment-card">
      <div class="exp-left">
        <div class="exp-id">최근 실행</div>
        <div class="exp-desc">
          <template v-if="runInfo">
            {{ OWNER_LABEL[runInfo.owner] || runInfo.owner }} · {{ runInfo.status === 'IN_PROGRESS' ? '진행중' : runInfo.status === 'COMPLETED' ? '완료' : runInfo.status === 'FAILED' ? '실패' : runInfo.status }}
            <div style="margin-top:6px; color:#9fb0c2; font-size:13px">
              시작: {{ toKST(runInfo.startedAt) }}
              <span v-if="runInfo.endedAt"> · 종료: {{ toKST(runInfo.endedAt) }}</span>
              <span style="margin-left:8px">소요: {{ runInfo && runInfo.startedAt ? prettyDuration(((runInfo.endedAt ? new Date(runInfo.endedAt).getTime() : Date.now()) - new Date(runInfo.startedAt).getTime())/1000) : '--' }}</span>
            </div>
          </template>
          <template v-else>
            <template v-if="lastRunNotReplay">최근 재생 실행 이력 없음</template>
            <template v-else>실행 이력 없음</template>
          </template>
        </div>
      </div>
      <div class="exp-right">
        <div v-if="runInfo" :class="['badge', runInfo.status === 'IN_PROGRESS' ? 'running' : runInfo.status === 'COMPLETED' ? 'pass' : 'failed']" style="padding:8px 14px; font-weight:700; border-radius:18px">
          {{ runInfo.status === 'IN_PROGRESS' ? '진행중' : runInfo.status === 'COMPLETED' ? '완료' : runInfo.status === 'FAILED' ? '실패' : runInfo.status }}
        </div>
        <div v-else class="badge pass">-</div>
      </div>
    </div>

    <div class="summary-grid">
      <div class="summary-card">
        <span class="dot" style="background:#3478f6"></span>
        <div class="title">접수 (Accepted)</div>
        <div class="value">{{ summary.accepted != null ? Number(summary.accepted).toLocaleString() : '--' }}</div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#2ed39a"></span>
        <div class="title">체결 (Filled)</div>
        <div class="value">{{ summary.filled != null ? Number(summary.filled).toLocaleString() : '--' }}</div>
        <div style="margin-top:8px; color:#9fb0c2; font-size:13px">체결률: <strong v-if="summary.accepted">{{ summary.accepted ? ((summary.filled || 0) / summary.accepted * 100).toFixed(2) : '0.00' }}%</strong><span v-else>--</span></div>
        <div style="margin-top:10px">
          <div class="progress-bar-track">
            <div class="progress-bar-fill" :style="{ width: summary.accepted ? Math.min(100, ((summary.filled||0) / (summary.accepted||1) * 100)) + '%' : '0%' }"></div>
          </div>
        </div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#ff9f43"></span>
        <div class="title">미체결 (Unfilled)</div>
        <div class="value">{{ summary.unfilled != null ? Number(summary.unfilled).toLocaleString() : '--' }}</div>
        <div style="margin-top:10px">
          <div class="progress-bar-track">
            <div class="progress-bar-fill" :style="{ width: summary.accepted ? Math.min(100, ((summary.unfilled||0) / (summary.accepted||1) * 100)) + '%' : '0%' , background: '#ff9f43' }"></div>
          </div>
        </div>
      </div>
    </div>

    <div class="mid-cards">
      <div class="left">
        <h4 class="card-title">목표 대비 처리 성능</h4>
        <div style="display:flex; flex-direction:column; gap:12px">
          <div>
            <div style="color:#9fb0c2; font-size:13px">평균 처리량 (평균 TPS)</div>
            <div style="font-weight:800; font-size:20px; margin-top:6px">{{ summary.accepted != null && runInfo && runInfo.startedAt ? ( ((summary.accepted||0) / Math.max(1, ((runInfo.endedAt ? new Date(runInfo.endedAt).getTime() : Date.now()) - new Date(runInfo.startedAt).getTime())/1000))).toFixed(2) : '--' }} req/s</div>
            <div class="progress-bar-track" style="margin-top:8px">
              <div class="progress-bar-fill" :style="{ width: summary.accepted && runInfo && runInfo.startedAt ? Math.min(100, ((summary.accepted||0) / Math.max(1, ((runInfo.endedAt ? new Date(runInfo.endedAt).getTime() : Date.now()) - new Date(runInfo.startedAt).getTime())/1000)) / 10000 * 100) + '%' : '0%' }"></div>
            </div>
            <div style="color:#9fb0c2; margin-top:6px">목표: 10,000건/초</div>
          </div>

          <div>
            <div style="color:#9fb0c2; font-size:13px">체결률</div>
            <div style="font-weight:800; font-size:20px; margin-top:6px">{{ summary.accepted ? ((summary.filled||0) / (summary.accepted||1) * 100).toFixed(2) + '%' : '--' }}</div>
            <div class="progress-bar-track" style="margin-top:8px">
              <div class="progress-bar-fill" :style="{ width: summary.accepted ? Math.min(100, ((summary.filled||0) / (summary.accepted||1) * 100) / 90 * 100) + '%' : '0%' }"></div>
            </div>
            <div style="color:#9fb0c2; margin-top:6px">목표: 90% <!-- 임시 목표치, 팀이 확정 전까지 주석으로 남김 --></div>
          </div>
        </div>
      </div>

      <div class="right">
        <h4 class="card-title">요약</h4>
        <div style="color:#9fb0c2">실측값과 목표값을 비교합니다. 실행 정보가 없거나 집계가 이루어지지 않으면 "데이터 없음"으로 표시됩니다.</div>
      </div>
    </div>

    <div class="trace-card">
      <h4 class="card-title">주문 처리 구간 추적</h4>
      <div class="trace-controls">
        <input v-model="traceId" type="text" class="trace-input" placeholder="Order ID를 입력하세요" />
        <button type="button" class="btn-primary" :disabled="uiState === 'loading'" @click="doSearch">검색</button>
      </div>

      <div class="trace-result">
        <div v-if="uiState === 'loading'" class="center-msg">조회 중입니다...</div>
        <div v-else-if="uiState === 'error'">
          <div class="search-msg">{{ searchMessage }}</div>
        </div>
        <div v-else-if="uiState === 'idle'">
          <div class="center-msg">주문 ID를 입력하고 검색하세요.</div>
        </div>
        <div v-else-if="uiState === 'success'">
          <div v-if="!traceData" class="center-msg">데이터 없음</div>
          <div v-else class="order-detail panel">
            <!-- 3-step timeline: Submitted -> Filled -> Canceled -->
            <div class="timeline">
              <div class="tl-step">
                <div class="circle" :style="{ background: '#3478f6' }">1</div>
                <div class="tl-name">접수</div>
                <div class="tl-time">{{ toKST(traceData.submittedAt) }}</div>
              </div>

              <div class="tl-step">
                <div class="circle" :style="{ background: (traceData.executions && traceData.executions.length ? '#2ed39a' : '#2c3a4a') }">2</div>
                <div class="tl-name">체결</div>
                <div class="tl-time">
                  <template v-if="traceData.executions && traceData.executions.length">{{ toKST(traceData.executions[traceData.executions.length - 1].executedAt) }}</template>
                  <template v-else>해당 없음</template>
                </div>
              </div>

              <div class="tl-step">
                <div class="circle" :style="{ background: (traceData.canceledAt ? '#ff6b6b' : '#2c3a4a') }">3</div>
                <div class="tl-name">취소</div>
                <div class="tl-time">{{ traceData.canceledAt ? toKST(traceData.canceledAt) : '해당 없음' }}</div>
              </div>
            </div>
            <div class="order-row"><strong>Order ID:</strong> {{ traceData.orderId }}</div>
            <div class="order-row"><strong>Market:</strong> {{ traceData.market || '--' }}</div>
            <div class="order-row"><strong>Side:</strong> {{ STATUS_LABEL[traceData.side] ? traceData.side : traceData.side }}</div>
            <div class="order-row"><strong>Price:</strong> {{ formatNumberString(traceData.price) }}</div>
            <div class="order-row"><strong>Quantity:</strong> {{ formatNumberString(traceData.quantity) }}</div>
            <div class="order-row"><strong>Remaining:</strong> {{ traceData.remainingQuantity != null ? formatNumberString(traceData.remainingQuantity) : '--' }}</div>
            <div class="order-row"><strong>Submitted At:</strong> {{ toKST(traceData.submittedAt) }}</div>
            <div class="order-row"><strong>Status:</strong> {{ STATUS_LABEL[traceData.status] || traceData.status }}</div>
            <div class="order-row"><strong>Mode:</strong> {{ MODE_LABEL[traceData.mode] || traceData.mode || '--' }}</div>
            <h5 style="margin-top:12px">Executions</h5>
            <div v-if="!traceData.executions || traceData.executions.length === 0">체결 내역이 없습니다.</div>
            <div v-else>
              <div v-for="(ex, idx) in traceData.executions" :key="idx" class="exec-row">
                <div><strong>Execution ID:</strong> {{ ex.executionId || ex.id || '--' }}</div>
                <div><strong>Executed At:</strong> {{ toKST(ex.executedAt) }}</div>
                <div><strong>Price:</strong> {{ formatNumberString(ex.price) }}</div>
                <div><strong>Quantity:</strong> {{ formatNumberString(ex.quantity) }}</div>
                <div><strong>Mode:</strong> {{ MODE_LABEL[ex.mode] || ex.mode || '--' }}</div>
                <div><strong>Buy Order:</strong> {{ ex.buyOrderId || '--' }}</div>
                <div><strong>Sell Order:</strong> {{ ex.sellOrderId || '--' }}</div>
              </div>
            </div>
          </div>
        </div>
        <div v-if="searchMessage && uiState !== 'error'" class="search-msg">{{ searchMessage }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.trt-page {
  width: 100%;
  max-width: none;
  min-width: 0;
  box-sizing: border-box;
  /* section gap used between top-level sections */
  --section-gap: 16px;
}

.trt-page > * + * {
  margin-top: var(--section-gap);
}

.page-header h2 {
  margin: 0 0 6px;
}
.page-header .subtitle {
  color: #9fb0c2;
  font-size: 13px;
  margin: 0;
}
.page-header hr {
  border: 0;
  height: 1px;
  background: #1b2e46;
  margin: 12px 0 0;
}

.experiment-card {
  width: 100%;
  min-height: 96px;
  box-sizing: border-box;
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  padding: 20px;
  border-radius: 12px;
}
.exp-id { font-weight: 700; font-size: 15px }
.exp-desc { color: #9fb0c2; margin-top: 6px; font-size: 13px }
.badge.pass { background: #072a1a; color: #2ed39a; padding: 6px 12px; border-radius: 20px; font-weight: 700; font-size: 12px }
.badge.recovered { background: #072a1a; color: #2ed39a; padding: 4px 12px; border-radius: 20px; font-weight: 700; font-size: 12px }
.badge.running { background: #2b2a10; color: #ffd166 }
.badge.failed { background: #2b0f12; color: #ff6b6b }

.summary-grid {
  display: grid;
  /* three summary cards side-by-side */
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}
.summary-card {
  position: relative;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 18px 20px;
  min-height: 140px; /* unify card height baseline */
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  /* distribute content so cards with extra controls keep balanced layout */
  justify-content: space-between;
}
.summary-card .dot {
  position: absolute;
  top: 16px;
  right: 16px;
  width: 10px;
  height: 10px;
  border-radius: 50%;
}
.summary-card .title { color: #9fb0c2; font-size: 13px }
.summary-card .value { font-weight: 800; font-size: 22px; margin-top: 10px }

.mid-cards {
  display: grid;
  grid-template-columns: 65% 35%;
  gap: 16px;
  /* stretch children to same height so left/right visually match */
  align-items: stretch;
}
.mid-cards .left,
.mid-cards .right {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
  min-height: 210px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  /* distribute content evenly so both boxes look consistent */
  justify-content: space-between;
  line-height: 1.45;
}
.card-title { margin: 0 0 12px; font-weight: 700; font-size: 15px }

.nfr-table { width: 100%; border-collapse: collapse }
.nfr-table thead th {
  text-align: left;
  color: #9fb0c2;
  font-size: 13px;
  font-weight: 600;
  padding: 0 6px 10px;
  border-bottom: 1px solid #172a3e;
}
.nfr-table tbody td {
  padding: 10px 6px;
  border-bottom: 1px solid #0b2534;
  font-size: 14px;
}
.nfr-table tbody tr:last-child td { border-bottom: 0 }
.nfr-name { color: #c9d6e3 }
.nfr-val { font-weight: 700 }

.fault-list { display: flex; flex-direction: column; gap: 10px }
.fault-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  background: #071826;
  border-radius: 8px;
}
.f-name { flex: 1; font-weight: 600; font-size: 14px }
.f-time { color: #ff9f43; font-weight: 700; font-size: 13px }

.trace-card {
  width: 100%;
  min-height: 220px;
  box-sizing: border-box;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  padding: 20px;
  border-radius: 12px;
}

/* Make vertical spacing between main sections consistent */
.experiment-card,
.summary-grid,
.mid-cards,
.trace-card {
  /* already have internal padding; ensure consistent outer spacing handled by --section-gap */
}

.trace-controls { display: flex; gap: 10px; align-items: center }
.trace-input {
  flex: 1;
  min-width: 0;
  height: 40px;
  padding: 0 12px;
  background: #071826;
  border: 1px solid #172a3e;
  color: #e6eef8;
  border-radius: 8px;
  box-sizing: border-box;
  font-size: 14px;
}
.trace-input:focus { outline: none; border-color: #3478f6 }

.btn-primary {
  flex: 0 0 120px;
  width: 120px;
  height: 40px;
  box-sizing: border-box;
  appearance: none;
  -webkit-appearance: none;
  -moz-appearance: none;
  background: #3478f6;
  color: #fff;
  border: 1px solid #3478f6;
  border-radius: 8px;
  font-weight: 700;
  font-size: 14px;
  cursor: pointer;
}
.btn-primary:hover { background: #2f68d1 }

.timeline {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin-top: 16px;
}
.tl-step {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 0 6px;
}
.tl-step:not(:last-child)::after {
  content: '';
  position: absolute;
  top: 20px;
  left: 50%;
  width: 100%;
  height: 2px;
  background: #172a3e;
  z-index: 0;
}
.circle {
  position: relative;
  z-index: 1;
  width: 40px;
  height: 40px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  color: #fff;
  margin-bottom: 10px;
}
.tl-name { font-weight: 600; font-size: 14px }
.tl-time { color: #9fb0c2; font-size: 13px; margin-top: 4px }

@media (max-width: 900px) {
  .summary-grid { grid-template-columns: repeat(2, minmax(0, 1fr)) }
  .mid-cards { grid-template-columns: 1fr }
  .timeline { grid-template-columns: 1fr; row-gap: 16px }
  .tl-step {
    flex-direction: row;
    align-items: center;
    justify-content: flex-start;
    text-align: left;
    gap: 12px;
    padding: 0;
  }
  .tl-step:not(:last-child)::after { display: none }
  .circle { margin-bottom: 0 }
}
  .order-placeholder, .order-detail {
    margin-top: 12px;
    padding: 12px;
    background: #071826;
    border-radius: 8px;
    border: 1px solid #173141;
    color: #cfe6fa;
  }
  .order-row { margin: 6px 0 }
  .exec-row { padding: 8px; border-top: 1px solid #0b2534; margin-top: 8px }
  .search-msg { margin-top: 12px; color: #ff9f43; font-weight: 700 }
</style>
