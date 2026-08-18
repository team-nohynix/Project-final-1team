<script setup lang="ts">
import { ref, onBeforeUnmount } from 'vue'

// input
const traceId = ref('')

// lightweight placeholders for other UI blocks (keeps page stable)
const experiment = ref<any>({ id: '--', name: '', rate: '--', duration: '--', markets: '--', status: '상태 확인 전' })
const summary = ref<any>({ total: null, accepted: null, rejected: null, errors: null })
const nfrs = ref<any[]>([])
const faults = ref<any[]>([])

// UI states: 'idle' | 'loading' | 'success' | 'error'
const uiState = ref<'idle' | 'loading' | 'success' | 'error'>('idle')
const searchMessage = ref('')

// response holder
const traceData = ref<any | null>(null)

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
})
</script>

<template>
  <div class="trt-page">
    <header class="page-header">
      <h2>테스트 결과·추적</h2>
      <p class="subtitle">성능 시험 결과, 장애 복구 기록, 주문 단위 구간 추적</p>
      <hr />
    </header>

    <div class="experiment-card">
      <div class="exp-left">
        <div class="exp-id">Experiment #{{ experiment?.id ?? '--' }}</div>
        <div class="exp-desc">{{ experiment?.name ?? '실험 정보 없음' }} · {{ experiment?.rate ?? '--' }} · {{ experiment?.duration ?? '--' }} · {{ experiment?.markets ?? '--' }} markets</div>
      </div>
      <div class="exp-right"><div class="badge pass">{{ experiment?.status ?? '상태 확인 전' }}</div></div>
    </div>

    <div class="summary-grid">
      <div class="summary-card">
        <span class="dot" style="background:#3478f6"></span>
        <div class="title">전체 입력 주문</div>
        <div class="value">{{ summary.total != null ? summary.total.toLocaleString() : '--' }}</div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#2ed39a"></span>
        <div class="title">접수 주문</div>
        <div class="value">{{ summary.accepted != null ? summary.accepted.toLocaleString() : '--' }}</div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#ff9f43"></span>
        <div class="title">과부하 거절(429)</div>
        <div class="value">{{ summary.rejected != null ? summary.rejected.toLocaleString() : '--' }}</div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#ff4b4b"></span>
        <div class="title">데이터 오류</div>
        <div class="value">{{ summary.errors != null ? summary.errors : '--' }}</div>
      </div>
    </div>

    <div class="mid-cards">
      <div class="left">
        <h4 class="card-title">비기능 요구사항 검증</h4>
        <table class="nfr-table">
          <thead>
            <tr>
              <th>항목</th>
              <th>측정값</th>
              <th>결과</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="nfrs.length === 0">
              <td colspan="3" class="nfr-val">데이터 없음</td>
            </tr>
            <tr v-for="(n,i) in nfrs" :key="i">
              <td class="nfr-name">{{ n.name }}</td>
              <td class="nfr-val">{{ n.value }}</td>
              <td class="nfr-res"><span class="badge pass">통과</span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="right">
        <h4 class="card-title">장애 주입 결과</h4>
        <div class="fault-list">
          <div v-if="faults.length === 0" class="fault-row">데이터 없음</div>
          <div v-else>
            <div v-for="(f,i) in faults" :key="i" class="fault-row">
              <div class="f-name">{{ f.name }}</div>
              <div class="f-time">{{ f.time }}</div>
              <span class="badge recovered">{{ f.result }}</span>
            </div>
          </div>
        </div>
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
}

.trt-page > * + * {
  margin-top: 16px;
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

.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
.summary-card {
  position: relative;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 18px;
  min-height: 90px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
  justify-content: flex-start;
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
  grid-template-columns: repeat(5, minmax(0, 1fr));
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
