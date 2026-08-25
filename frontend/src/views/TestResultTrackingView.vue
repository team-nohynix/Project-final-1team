<script setup lang="ts">
import { ref, onBeforeUnmount, onMounted, computed } from 'vue'

// run / summary state
const runInfo = ref<any | null>(null)
const runLoading = ref(false)
const lastRunNotReplay = ref(false)
const summary = ref<any>({ accepted: null, filled: null, unfilled: null })

// labels
const OWNER_LABEL: Record<string, string> = {
  trader: '페이퍼 트레이딩',
  replayengine: '주문 재생',
}

// run history for selector
const runHistory = ref<Array<any>>([])
const selectedRunId = ref<string>('')

// polling handle for last-run
let pollInterval: number | null = null

function formatNumberString(s: string) {
  if (s == null) return '--'
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
    const kst = new Date(d.getTime() + 9 * 60 * 60 * 1000)
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${kst.getFullYear()}-${pad(kst.getMonth() + 1)}-${pad(kst.getDate())} ${pad(kst.getHours())}:${pad(kst.getMinutes())}:${pad(kst.getSeconds())}`
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

async function loadRunHistory() {
  try {
    const res = await fetch('/order-api/v1/sessions/runs')
    if (!res.ok) return
    const data = await res.json()
    runHistory.value = Array.isArray(data) ? data : []
    // default selection: prefer current runInfo.runId then first
    if (runInfo.value && runInfo.value.runId) {
      const found = runHistory.value.find((r: any) => r.runId === runInfo.value.runId)
      if (found) selectedRunId.value = found.runId
    }
    if (!selectedRunId.value && runHistory.value.length) selectedRunId.value = runHistory.value[0].runId
  } catch (e) {
    // ignore
  }
}

function selectRun(runId: string) {
  const found = runHistory.value.find(r => r.runId === runId)
  if (found) {
    runInfo.value = found
    fetchSummary()
  }
}

onBeforeUnmount(() => {
  if (pollInterval) {
    try { clearInterval(pollInterval) } catch (_) {}
    pollInterval = null
  }
})

onMounted(() => {
  fetchLastRun()
  loadRunHistory()
})

function prettyDuration(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '--'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  const parts: string[] = []
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

// derived metrics (reuse same formulas as template)
const TPS_GOAL = 10000
const FILL_GOAL = 90 // percent

const averageTPS = computed(() => {
  if (summary.value.accepted == null || !runInfo.value || !runInfo.value.startedAt) return null
  const durationSec = Math.max(1, ((runInfo.value.endedAt ? new Date(runInfo.value.endedAt).getTime() : Date.now()) - new Date(runInfo.value.startedAt).getTime())/1000)
  return ((summary.value.accepted||0) / durationSec)
})

const fillRate = computed(() => {
  if (!summary.value || summary.value.accepted == null) return null
  return (summary.value.filled || 0) / (summary.value.accepted || 1) * 100
})

const tpsPass = computed(() => {
  return averageTPS.value != null && averageTPS.value >= TPS_GOAL
})

const fillPass = computed(() => {
  return fillRate.value != null && fillRate.value >= FILL_GOAL
})

const overallState = computed(() => {
  // If both key metrics are completely absent, show 'no-data'
  if (averageTPS.value == null && fillRate.value == null) return 'no-data'
  return (tpsPass.value && fillPass.value) ? 'pass' : 'fail'
})
</script>

<template>
  <div class="trt-page">
    <header class="page-header">
      <h2>리플레이 결과 분석</h2>
      <p class="subtitle">리플레이 실행 결과와 목표 대비 성능을 확인합니다.</p>
      <hr />
    </header>

    <div class="trace-card">
      <h4 class="card-title">리플레이별 결과 조회</h4>
      <div style="display:flex; gap:8px; align-items:center; margin-bottom:8px">
        <select v-model="selectedRunId" @change="selectRun(selectedRunId)" style="flex:1; height:40px; background:#071826; border:1px solid #172a3e; color:#e6eef8; padding:0 10px; border-radius:8px">
          <option v-for="run in runHistory" :key="run.runId" :value="run.runId">
            {{ toKST(run.startedAt) }} · {{ run.status === 'IN_PROGRESS' ? '진행중' : run.status === 'COMPLETED' ? '완료' : run.status }}
          </option>
        </select>
        <button class="btn-primary" type="button" @click="loadRunHistory">조회</button>
      </div>
      <div style="color:#9fb0c2; font-size:13px">선택한 실행의 집계를 오른쪽 상단 요약과 목표 비교 카드에서 확인합니다.</div>
    </div>

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
          <div style="display:flex; align-items:center; gap:10px; margin-bottom:6px">
          <div style="flex:1"></div>
          <div :class="['overall-badge', overallState === 'pass' ? 'pass' : overallState === 'fail' ? 'fail' : 'no-data']" style="font-weight:800; padding:6px 12px; border-radius:14px">
            <template v-if="overallState === 'pass'">종합 목표 달성</template>
            <template v-else-if="overallState === 'fail'">종합 목표 미달</template>
            <template v-else>데이터 없음</template>
          </div>
        </div>
        <div style="display:flex; flex-direction:row; gap:12px">
          <div style="flex:1">
            <div style="color:#9fb0c2; font-size:13px">평균 처리량 (평균 TPS)</div>
            <div :style="{ fontWeight: 800, fontSize: '20px', marginTop: '6px', color: (averageTPS != null ? (tpsPass ? '#2ed39a' : '#ff6b6b') : undefined) }">{{ averageTPS != null ? averageTPS.toFixed(2) : '--' }} req/s <small :style="{ color: (averageTPS != null ? (tpsPass ? '#2ed39a' : '#ff6b6b') : '#9fb0c2'), marginLeft: '8px', fontSize: '12px' }">{{ averageTPS != null ? (tpsPass ? '달성' : '미달') : '' }}</small></div>
            <div class="progress-bar-track" style="margin-top:8px">
              <div class="progress-bar-fill" :style="{ width: summary.accepted && runInfo && runInfo.startedAt ? Math.min(100, ((summary.accepted||0) / Math.max(1, ((runInfo.endedAt ? new Date(runInfo.endedAt).getTime() : Date.now()) - new Date(runInfo.startedAt).getTime())/1000)) / 10000 * 100) + '%' : '0%' }"></div>
            </div>
          </div>

          <div style="flex:1">
            <div style="color:#9fb0c2; font-size:13px">체결률</div>
            <div :style="{ fontWeight: 800, fontSize: '20px', marginTop: '6px', color: (fillRate != null ? (fillPass ? '#2ed39a' : '#ff6b6b') : undefined) }">{{ fillRate != null ? fillRate.toFixed(2) + '%' : '--' }} <small :style="{ color: (fillRate != null ? (fillPass ? '#2ed39a' : '#ff6b6b') : '#9fb0c2'), marginLeft: '8px', fontSize: '12px' }">{{ fillRate != null ? (fillPass ? '달성' : '미달') : '' }}</small></div>
            <div class="progress-bar-track" style="margin-top:8px">
              <div class="progress-bar-fill" :style="{ width: summary.accepted ? Math.min(100, ((summary.filled||0) / (summary.accepted||1) * 100) / 90 * 100) + '%' : '0%' }"></div>
            </div>
          </div>
        </div>
      </div>
    </div>

    
  </div>
</template>

<style scoped>
.overall-badge.pass { background: #2ed39a; color: #052018 }
/* progress bar visuals */
.progress-bar-track {
  background: #0b1e2f;
  height: 6px;
  border-radius: 3px;
  overflow: hidden;
}
.progress-bar-fill {
  height: 100%;
  background: #3478f6;
  border-radius: 3px;
  transition: width 0.3s ease;
}
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
  /* distribute content starting from top; use gap for spacing between items */
  justify-content: flex-start;
  gap: 16px;
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
  grid-template-columns: 1fr;
  gap: 16px;
  /* stretch child to full width */
  align-items: stretch;
}
.mid-cards .left {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
  min-height: 140px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  /* distribute content starting from top; use gap for spacing between items */
  justify-content: flex-start;
  gap: 16px;
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
