<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'

defineOptions({ name: 'MarketStreamView' })

const collector = ref({
  name: '업비트 시세 수집기',
  markets: 20,
  lastMessage: '42ms ago',
  reconnectPolicy: '≤ 30 sec',
})

// removed mock metrics UI (backend-driven view only)

// manifest market type
type ManifestMarket = { market: string; batchUrl: string; streamUrl: string }
const marketOptions = ref<ManifestMarket[]>([])

const selectedMarket = ref('KRW-BTC')
const selectedDate = ref<string>('')

const loading = ref(false)
const errorMessage = ref('')
const status = ref<'idle' | 'loading' | 'success' | 'error'>('idle')

// Batch types
interface Candle { ts: number; open: number; high: number; low: number; close: number; volume: number }
interface BatchResponse { market: string; range: { start: string; end: string }; candles: { days: Candle[]; weeks: Candle[]; months: Candle[]; years: Candle[] } }
const candles = ref<BatchResponse['candles'] | null>(null)
const rangeText = ref('')

// Stream types
type StreamEventType = 'candle_second' | 'candle_minute'
interface StreamEvent { type: StreamEventType; ts: number; open: number; high: number; low: number; close: number; volume: number }
interface StreamResponse { market: string; range: { start: string; end: string }; events: StreamEvent[] }

const streamData = ref<StreamResponse | null>(null)
const activeTab = ref<'batch' | 'stream'>('batch')
const activeFilter = ref<'all' | 'second' | 'minute'>('all')

function kstDateString(offsetDays = 0) {
  const now = new Date()
  const kst = new Date(now.toLocaleString('en-US', { timeZone: 'Asia/Seoul' }))
  kst.setDate(kst.getDate() + offsetDays)
  return kst.toISOString().slice(0, 10)
}

// default: yesterday in Asia/Seoul
selectedDate.value = kstDateString(-1)

async function fetchManifest(date: string) {
  errorMessage.value = ''
  try {
    const res = await fetch(`/v1/markets/data?date=${encodeURIComponent(date)}`)
    if (!res.ok) {
      const txt = await res.text().catch(() => '')
      throw new Error(`HTTP ${res.status} ${txt}`)
    }
    const j = (await res.json()) as { date?: string; markets?: ManifestMarket[] }
    marketOptions.value = j.markets || []
    // ensure default selection exists
    if (!marketOptions.value.find((m) => m.market === selectedMarket.value) && marketOptions.value.length) {
      selectedMarket.value = marketOptions.value[0].market
    }
  } catch (err: unknown) {
    const e = err as Error
    errorMessage.value = e?.message ?? String(err)
  }
}

async function fetchBatch(market: string, date: string) {
  loading.value = true
  status.value = 'loading'
  errorMessage.value = ''
  candles.value = null
  rangeText.value = ''
  try {
    const res = await fetch(`/v1/markets/${encodeURIComponent(market)}/batch?date=${encodeURIComponent(date)}`)
    if (!res.ok) {
      const txt = await res.text().catch(() => '')
      throw new Error(`HTTP ${res.status} ${txt}`)
    }
    const j = (await res.json()) as BatchResponse
    candles.value = j.candles
    // set range display if provided
    if (j.range && j.range.start && j.range.end) {
      rangeText.value = `${formatKST(j.range.start)} ~ ${formatKST(j.range.end)} KST`
    }
    // update last lookup time (KST)
    collector.value && (collector.value.lastMessage = new Date().toLocaleTimeString('ko-KR', { timeZone: 'Asia/Seoul' }))
    status.value = 'success'
  } catch (err: unknown) {
    const e = err as Error
    errorMessage.value = e?.message ?? String(err)
    status.value = 'error'
  } finally {
    loading.value = false
  }
}

async function fetchStream(market: string, date: string) {
  loading.value = true
  status.value = 'loading'
  errorMessage.value = ''
  streamData.value = null
  rangeText.value = ''
  try {
    const res = await fetch(`/v1/markets/${encodeURIComponent(market)}/stream?date=${encodeURIComponent(date)}`)
    if (!res.ok) {
      const txt = await res.text().catch(() => '')
      throw new Error(`HTTP ${res.status} ${txt}`)
    }
    const j = (await res.json()) as StreamResponse
    streamData.value = j
    if (j.range && j.range.start && j.range.end) {
      rangeText.value = `${formatKST(j.range.start)} ~ ${formatKST(j.range.end)} KST`
    }
    collector.value && (collector.value.lastMessage = new Date().toLocaleTimeString('ko-KR', { timeZone: 'Asia/Seoul' }))
    status.value = 'success'
  } catch (err: unknown) {
    const e = err as Error
    errorMessage.value = e?.message ?? String(err)
    status.value = 'error'
  } finally {
    loading.value = false
  }
}

function formatKST(iso: string) {
  try {
    const d = new Date(iso)
    const kst = new Date(d.toLocaleString('en-US', { timeZone: 'Asia/Seoul' }))
    const Y = kst.getFullYear()
    const M = String(kst.getMonth() + 1).padStart(2, '0')
    const D = String(kst.getDate()).padStart(2, '0')
    const hh = String(kst.getHours()).padStart(2, '0')
    const mm = String(kst.getMinutes()).padStart(2, '0')
    return `${Y}-${M}-${D} ${hh}:${mm}`
  } catch {
    return iso
  }
}

onMounted(() => {
  fetchManifest(selectedDate.value)
})

// reset results when market or date change
watch([selectedMarket, selectedDate], () => {
  candles.value = null
  streamData.value = null
  errorMessage.value = ''
  status.value = 'idle'
  activeFilter.value = 'all'
})

watch(activeTab, () => {
  activeFilter.value = 'all'
})

// computed helpers for stream
const totalEvents = computed(() => streamData.value ? streamData.value.events.length : 0)
const secondCount = computed(() => streamData.value ? streamData.value.events.filter(e => e.type === 'candle_second').length : 0)
const minuteCount = computed(() => streamData.value ? streamData.value.events.filter(e => e.type === 'candle_minute').length : 0)

const filteredLatest = computed(() => {
  const events = streamData.value ? streamData.value.events : []
  let filtered = events
  if (activeFilter.value === 'second') filtered = events.filter(e => e.type === 'candle_second')
  else if (activeFilter.value === 'minute') filtered = events.filter(e => e.type === 'candle_minute')
  // sort copy by ts desc and take 50
  const copy = filtered.slice()
  copy.sort((a, b) => b.ts - a.ts)
  return copy.slice(0, 50)
})

function formatEventTime(e: StreamEvent) {
  const d = new Date(e.ts)
  const kst = new Date(d.toLocaleString('en-US', { timeZone: 'Asia/Seoul' }))
  const hh = String(kst.getHours()).padStart(2, '0')
  const mm = String(kst.getMinutes()).padStart(2, '0')
  const ss = String(kst.getSeconds()).padStart(2, '0')
  if (e.type === 'candle_second') return `${hh}:${mm}:${ss}`
  return `${hh}:${mm}`
}

</script>

<template>
  <div class="market-page">
    <header class="market-header">
      <h2 class="market-title">시세 데이터 조회</h2>
      <p class="market-subtitle">업비트 과거 시세 데이터 조회 및 확인</p>
      <hr class="market-divider" />
    </header>

    <div class="market-collector-card">
      <div class="market-collector-left">
        <div class="market-collector-name">{{ collector.name }}</div>
        <div class="market-collector-status">
          <span class="market-status-dot" :class="{ success: status === 'success', error: status === 'error', idle: status === 'idle', loading: status === 'loading' }"></span>
          <span class="market-status-text" :class="{ off: status === 'error' }">{{ status === 'idle' ? '조회 전' : status === 'loading' ? '조회 중' : status === 'success' ? '조회 성공' : '조회 실패' }}</span>
          <span class="market-collector-meta">{{ collector.markets }} KRW markets · 마지막 조회 {{ collector.lastMessage }}</span>
        </div>
      </div>
    </div>

    <!-- metrics removed: backend-driven view only -->

    <div class="market-bottom-grid">
      <div class="market-list-card">
        <h4 class="market-card-title">마켓별 시세 데이터</h4>

        <div class="market-tabs">
          <button type="button" :class="['tab', { active: activeTab === 'batch' }]" @click="activeTab = 'batch'">Batch</button>
          <button type="button" :class="['tab', { active: activeTab === 'stream' }]" @click="activeTab = 'stream'">Stream</button>
        </div>

        <div class="market-controls">
          <label>
            마켓
            <select v-model="selectedMarket">
              <option v-for="(m, i) in marketOptions" :key="i" :value="m.market">{{ m.market }}</option>
            </select>
          </label>

          <label>
            날짜 (KST 기준)
            <input type="date" v-model="selectedDate" />
          </label>

          <button class="market-fetch-btn" :disabled="loading" @click="activeTab === 'batch' ? fetchBatch(selectedMarket, selectedDate) : fetchStream(selectedMarket, selectedDate)">
            {{ loading ? '조회 중...' : '시세 조회' }}
          </button>
        </div>

        <div v-if="errorMessage" class="market-error">오류: {{ errorMessage }}</div>

        <div class="market-data">
          <div class="market-data-meta">
            Collector: {{ collector.name }} · 상태: {{ status === 'idle' ? '조회 전' : status === 'loading' ? '조회 중' : status === 'success' ? '조회 성공' : '조회 실패' }} · 마지막 조회: {{ collector.lastMessage }}
            <div v-if="rangeText" style="margin-top:6px; color:#9fb0c2">조회 구간: {{ rangeText }}</div>
          </div>
          <div style="margin-top:6px; color:#9fb0c2; font-size:12px">단위: KRW</div>
          <!-- Batch view -->
          <div v-if="activeTab === 'batch'">
            <table class="market-data-table">
              <thead>
                <tr>
                  <th>구분</th>
                  <th>시가</th>
                  <th>고가</th>
                  <th>저가</th>
                  <th>종가</th>
                  <th>거래량</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>일봉</td>
                  <td>{{ candles?.days?.[0] ? Number(candles.days[0].open).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.days?.[0] ? Number(candles.days[0].high).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.days?.[0] ? Number(candles.days[0].low).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.days?.[0] ? Number(candles.days[0].close).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.days?.[0] && candles.days[0].volume != null ? Number(candles.days[0].volume).toLocaleString('ko-KR', { maximumFractionDigits: 4 }) : '-' }}</td>
                </tr>

                <tr>
                  <td>주봉</td>
                  <td>{{ candles?.weeks?.[0] ? Number(candles.weeks[0].open).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.weeks?.[0] ? Number(candles.weeks[0].high).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.weeks?.[0] ? Number(candles.weeks[0].low).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.weeks?.[0] ? Number(candles.weeks[0].close).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.weeks?.[0] && candles.weeks[0].volume != null ? Number(candles.weeks[0].volume).toLocaleString('ko-KR', { maximumFractionDigits: 4 }) : '-' }}</td>
                </tr>

                <tr>
                  <td>월봉</td>
                  <td>{{ candles?.months?.[0] ? Number(candles.months[0].open).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.months?.[0] ? Number(candles.months[0].high).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.months?.[0] ? Number(candles.months[0].low).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.months?.[0] ? Number(candles.months[0].close).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.months?.[0] && candles.months[0].volume != null ? Number(candles.months[0].volume).toLocaleString('ko-KR', { maximumFractionDigits: 4 }) : '-' }}</td>
                </tr>

                <tr>
                  <td>연봉</td>
                  <td>{{ candles?.years?.[0] ? Number(candles.years[0].open).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.years?.[0] ? Number(candles.years[0].high).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.years?.[0] ? Number(candles.years[0].low).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.years?.[0] ? Number(candles.years[0].close).toLocaleString('ko-KR') : '-' }}</td>
                  <td>{{ candles?.years?.[0] && candles.years[0].volume != null ? Number(candles.years[0].volume).toLocaleString('ko-KR', { maximumFractionDigits: 4 }) : '-' }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- Stream view -->
          <div v-if="activeTab === 'stream'">
            <div class="stream-summary-row">
              <div class="stream-card blue">
                <div class="stream-card-title">전체 이벤트</div>
                <div class="stream-card-value">{{ totalEvents.toLocaleString('ko-KR') }}건</div>
              </div>
              <div class="stream-card teal">
                <div class="stream-card-title">초봉</div>
                <div class="stream-card-value">{{ secondCount.toLocaleString('ko-KR') }}건</div>
              </div>
              <div class="stream-card green">
                <div class="stream-card-title">분봉</div>
                <div class="stream-card-value">{{ minuteCount.toLocaleString('ko-KR') }}건</div>
              </div>
            </div>

            <div class="stream-filters">
              <button :class="['pill', { active: activeFilter === 'all' }]" @click="activeFilter = 'all'">전체</button>
              <button :class="['pill', { active: activeFilter === 'second' }]" @click="activeFilter = 'second'">초봉</button>
              <button :class="['pill', { active: activeFilter === 'minute' }]" @click="activeFilter = 'minute'">분봉</button>
            </div>

            <div class="stream-table-meta">
              <div></div>
              <div class="stream-meta-text">
                <template v-if="activeFilter === 'all'">전체 {{ totalEvents.toLocaleString('ko-KR') }}건 중 최신 50건 표시</template>
                <template v-else-if="activeFilter === 'second'">초봉 {{ secondCount.toLocaleString('ko-KR') }}건 중 최신 50건 표시</template>
                <template v-else>분봉 {{ minuteCount.toLocaleString('ko-KR') }}건 중 최신 50건 표시</template>
              </div>
            </div>

            <div class="stream-table-wrap">
              <table class="market-data-table stream-table">
                <thead>
                  <tr>
                    <th>시간 (KST)</th>
                    <th>구분</th>
                    <th>시가</th>
                    <th>고가</th>
                    <th>저가</th>
                    <th>종가</th>
                    <th>거래량</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="(e, i) in filteredLatest" :key="`${e.type}-${e.ts}-${i}`">
                    <td>{{ formatEventTime(e) }}</td>
                    <td>{{ e.type === 'candle_second' ? '초봉' : '분봉' }}</td>
                    <td>{{ Number(e.open).toLocaleString('ko-KR') }}</td>
                    <td>{{ Number(e.high).toLocaleString('ko-KR') }}</td>
                    <td>{{ Number(e.low).toLocaleString('ko-KR') }}</td>
                    <td>{{ Number(e.close).toLocaleString('ko-KR') }}</td>
                    <td>{{ Number(e.volume).toLocaleString('ko-KR', { maximumFractionDigits: 4 }) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </div>

      <!-- subscription card removed to match backend-driven view -->
    </div>
  </div>
</template>

<style scoped>
.market-page {
  width: 100%;
  max-width: none;
  min-width: 0;
  box-sizing: border-box;
  padding-bottom: 32px;
}
.market-page > * + * {
  margin-top: 16px;
}

.market-title { margin: 0 0 6px }
.market-subtitle { color: #9fb0c2; font-size: 13px; margin: 0 }
.market-divider {
  border: 0;
  height: 1px;
  background: #1b2e46;
  margin: 12px 0 0;
}

.market-card-title { margin: 0 0 14px; font-weight: 700; font-size: 15px }

.market-collector-card {
  width: 100%;
  min-height: 100px;
  box-sizing: border-box;
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
}
.market-collector-name { font-weight: 700; font-size: 15px }
.market-collector-status {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  font-size: 13px;
  color: #9fb0c2;
}
.market-status-dot { width: 8px; height: 8px; border-radius: 50%; background: #556172; flex: 0 0 auto }
.market-status-dot.on { display: none }
.market-status-text { color: #2ed39a; font-weight: 700 }
.market-status-text.off { color: #ff5c5c }
.market-collector-meta { color: #9fb0c2 }

/* right-side reconnect UI removed */

/* metrics removed */

.market-bottom-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 16px;
  align-items: stretch;
}
.market-list-card {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
  min-height: 380px;
  box-sizing: border-box;
}

.market-row {
  display: grid;
  grid-template-columns: 1fr 1.2fr 1fr 1fr;
  align-items: center;
  padding: 12px 4px;
  border-bottom: 1px solid #0b2534;
  font-size: 14px;
}
.market-row:last-child { border-bottom: 0 }
.market-row-head {
  color: #9fb0c2;
  font-size: 12px;
  border-bottom: 1px solid #172a3e;
  padding-bottom: 8px;
}
.market-name { font-weight: 600 }
.market-price { color: #e6eef8 }
.market-latency-badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: #2ed39a;
  font-weight: 600;
  font-size: 13px;
}
.market-latency-badge .dot { width: 6px; height: 6px; border-radius: 50%; background: #2ed39a }
.market-latency-badge.warn { color: #ff9f43 }
.market-latency-badge.warn .dot { background: #ff9f43 }
.market-ws { color: #20c8e8; font-weight: 600 }


/* removed subscription and mock UI CSS */

.market-status-dot { width: 8px; height: 8px; border-radius: 50%; background: #556172; flex: 0 0 auto }
.market-status-dot.success { background: #2ed39a }
.market-status-dot.error { background: #ff5c5c }
.market-status-dot.loading { background: #f6c44d }
.market-status-dot.idle { background: #556172 }
.market-status-text { color: #2ed39a; font-weight: 700 }
.market-status-text.off { color: #ff5c5c }

.market-controls {
  display: grid;
  grid-template-columns: 120px 170px minmax(240px, 1fr);
  gap: 12px;
  align-items: end;
  margin-bottom: 12px;
}
.market-controls label { color: #9fb0c2; font-size: 13px; display: flex; flex-direction: column; gap: 6px }
.market-controls select,
.market-controls input[type="date"] { background: #071826; border: 1px solid #172a3e; color: #fff; padding: 0 10px; border-radius: 8px; height: 42px; appearance: none }

.market-controls label > select,
.market-controls label > input { height: 42px }
.market-error { color: #ff9f9f; background: rgba(255,95,95,0.04); padding: 8px 10px; border-radius: 8px; margin-bottom: 10px }
.market-data-meta { color: #9fb0c2; font-size: 12px; margin-bottom: 8px }
.market-data-table { width: 100%; border-collapse: collapse; font-size: 13px }
.market-data-table th { text-align: left; color: #9fb0c2; padding: 8px 6px; font-weight: 700 }
.market-data-table td { padding: 8px 6px; border-top: 1px solid #0b2534 }

/* Fetch button specific styles */
.market-fetch-btn {
  width: 100%;
  height: 42px;
  background: #132a46;
  border: 1px solid #1d3858;
  border-radius: 8px;
  color: #ffffff;
  font-size: 13px;
  font-weight: 700;
  cursor: pointer;
  appearance: none;
  -webkit-appearance: none;
  -moz-appearance: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
}
.market-fetch-btn:hover:not(:disabled) {
  background: #19385c;
  border-color: #3478f6;
}
.market-fetch-btn:active:not(:disabled) { filter: brightness(0.92) }
.market-fetch-btn:disabled { opacity: 0.6; cursor: not-allowed }

/* Tabs */
.market-tabs { display: flex; gap: 12px; margin-bottom: 12px }
.market-tabs .tab { background: transparent; border: 0; color: #9fb0c2; padding: 8px 10px; cursor: pointer; font-weight: 700 }
.market-tabs .tab.active { color: #3478f6; position: relative }
.market-tabs .tab.active::after { content: ''; position: absolute; left: 0; right: 0; bottom: -12px; height: 3px; background: #3478f6; border-radius: 3px }

/* Stream summary */
.stream-summary-row { display: flex; gap: 12px; margin: 12px 0 }
.stream-card { flex: 1; background: #071826; border: 1px solid #172a3e; border-radius: 10px; padding: 14px }
.stream-card-title { color: #9fb0c2; font-size: 13px }
.stream-card-value { color: #fff; font-size: 20px; font-weight: 800; margin-top: 6px }
.stream-card.blue { border-color: #3478f6 }
.stream-card.teal { border-color: #2ed39a }
.stream-card.green { border-color: #20c8e8 }

.stream-filters { display: flex; gap: 8px; margin: 10px 0 }
.pill { background: transparent; border: 1px solid #172a3e; color: #9fb0c2; padding: 6px 12px; border-radius: 20px; cursor: pointer }
.pill.active { background: #183652; border-color: #3478f6; color: #fff }

.stream-table-meta { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px }
.stream-meta-text { color: #9fb0c2; font-size: 13px }
.stream-table-wrap { overflow-x: auto; }
.stream-table { min-width: 880px }



@media (max-width: 900px) {
  /* responsive tweaks */
  .market-metrics-grid { display: none }
  .market-bottom-grid { grid-template-columns: 1fr }
  .market-row { grid-template-columns: 1fr 1fr; row-gap: 4px }
  .market-row-head { display: none }
}

@media (max-width: 800px) {
  .market-controls { grid-template-columns: 1fr }
  .market-controls label > select,
  .market-controls label > input,
  .market-fetch-btn { height: 42px }
}
@media (max-width: 900px) {
  .stream-summary-row { flex-direction: column; }
}
</style>
