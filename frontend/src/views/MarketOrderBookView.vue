<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'

// Use the backend's 20 market codes directly
const markets = ref([
  'KRW-USDT',
  'KRW-BTC',
  'KRW-XRP',
  'KRW-ETH',
  'KRW-ONDO',
  'KRW-LA',
  'KRW-SHIB',
  'KRW-RE',
  'KRW-DOGE',
  'KRW-SLX',
  'KRW-KAITO',
  'KRW-SOL',
  'KRW-XLM',
  'KRW-WLD',
  'KRW-MIRA',
  'KRW-ERA',
  'KRW-ADA',
  'KRW-AI',
  'KRW-NEAR',
  'KRW-ARX',
])

// default selected market is KRW-BTC
const selected = ref('KRW-BTC')

// orderbook data will be fetched from API; initial mock storage removed


// Reactive state for fetched orderbook
const orderbook = ref({ bids: [], asks: [], market: '', timestamp: '' })
const loading = ref(false)
const error = ref(false)
const errorMessage = ref('')
// track whether we have had a successful fetch recently
const lastSuccess = ref(false)

// polling interval id
let pollId = null
// prevent overlapping requests
const isFetching = ref(false)

const selectedBook = computed(() => orderbook.value)

// No UI-to-API mapping needed: UI uses the same 'KRW-XXX' codes as the API

const safeNum = (s) => {
  const n = typeof s === 'number' ? s : parseFloat(s)
  return Number.isFinite(n) ? n : 0
}

async function fetchOrderbookFor(symbol, { background = false, isInit = false } = {}) {
  // skip overlapping requests
  if (isFetching.value) return
  isFetching.value = true

  const apiId = symbol
  const url = `/order-api/v1/markets/${encodeURIComponent(apiId)}/orderbook?depth=20`
  // only show global loading when not a background poll (initial enter or market change)
  if (!background) {
    loading.value = true
    error.value = false
    errorMessage.value = ''
  }
  try {
    const res = await fetch(url)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = await res.json()

    // parse bids/asks (API provides strings for price/quantity)
    const bids = Array.isArray(data.bids) ? data.bids.map((b) => ({
      price: safeNum(b.price),
      qty: safeNum(b.quantity),
    })) : []
    const asks = Array.isArray(data.asks) ? data.asks.map((a) => ({
      price: safeNum(a.price),
      qty: safeNum(a.quantity),
    })) : []

    // compute cumulative quantities in provided order
    let cum = 0
    for (let i = 0; i < bids.length; i++) {
      cum += bids[i].qty
      bids[i].cum = cum
    }
    cum = 0
    for (let i = 0; i < asks.length; i++) {
      cum += asks[i].qty
      asks[i].cum = cum
    }

    // on success, replace bids/asks and update metadata
    orderbook.value.market = data.market || apiId
    orderbook.value.timestamp = data.timestamp || ''
    orderbook.value.bids = bids
    orderbook.value.asks = asks

    // mark fetch as successful
    error.value = false
    errorMessage.value = ''
    lastSuccess.value = true
  } catch (err) {
    // keep existing orderbook data on failure; only update connection state
    error.value = true
    errorMessage.value = '호가 데이터를 불러오지 못했습니다.'
    console.error('Orderbook fetch error', err)
    lastSuccess.value = false
  } finally {
    isFetching.value = false
    if (!background) loading.value = false
  }
}

// current price / change are not available; do not fabricate values

const selectMarket = (s) => {
  selected.value = s
}

// polling: fetch immediately and then every 1s
onMounted(() => {
  // initial fetch for default selected (show loading)
  fetchOrderbookFor(selected.value, { background: false, isInit: true })
  // background polling every 1s
  pollId = setInterval(() => fetchOrderbookFor(selected.value, { background: true }), 1000)
})

onUnmounted(() => {
  if (pollId) clearInterval(pollId)
})

// when user selects a different market, fetch immediately
watch(selected, (nv) => {
  // user changed market: show loading while fetching
  fetchOrderbookFor(nv, { background: false, isInit: true })
})

 

// Helpers for formatted displays and parsing
const formatShort = (n) => {
  if (!n && n !== 0) return ''
  if (n >= 1000000) return (n / 1000000).toFixed(2) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(0) + 'K'
  return n.toLocaleString()
}

const formatNumber = (n) => {
  if (n === null || n === undefined) return ''
  return Number(n).toLocaleString()
}

// Format quantities and cumulative quantities:
// - show up to 8 decimal places
// - trim unnecessary trailing zeros (e.g. 0.0010 -> 0.001)
// - do not apply thousands separators (keep raw numeric style)
const formatQuantity = (v) => {
  if (v === null || v === undefined || v === '') return ''
  const n = Number(v)
  if (!Number.isFinite(n)) return String(v)
  // Use fixed 8 decimals then trim trailing zeros and optional dot
  let s = n.toFixed(8)
  s = s.replace(/\.?(0+)$/, '') // remove trailing zeros and dot if needed
  // If the regex above removed all decimals (e.g. "123.00000000" -> "123"), ensure no trailing dot
  s = s.replace(/\.$/, '')
  return s
}

const apiBadgeText = computed(() => {
  if (loading.value) return '호가 API 연결 중'
  if (error.value) return '호가 API 연결 실패'
  if (lastSuccess.value) return '호가 API 연결됨'
  return '호가 API 연결 대기'
})

const apiBadgeClass = computed(() => {
  if (loading.value) return 'loading'
  if (error.value) return 'error'
  if (lastSuccess.value) return 'success'
  return ''
})


</script>

<template>
  <div class="market-page">
    <header class="page-header">
      <h2>마켓·호가창</h2>
      <p class="desc">마켓별 호가창과 실시간 체결 흐름</p>
      <hr class="divider" />
    </header>

    <div class="summary-bar">
      <div class="summary-left">
        <div class="symbol">{{ selected }}</div>
      </div>
          <div class="summary-right">
            <span class="live-dot-small"></span>
            <div class="live-badge">LIVE</div>
            <div :class="['api-badge', apiBadgeClass]">{{ apiBadgeText }}</div>
          </div>
    </div>

    <section class="content">
      <aside class="col col-markets">
        <div class="left-card">
          <div class="left-card-header"><h4>마켓·호가창</h4></div>
            <div class="markets-list scrollable-markets">
              <div
                v-for="m in markets"
                :key="m"
                :class="['market-row', { active: m === selected } ]"
                @click="selectMarket(m)"
              >
                <div class="mr-left">
                  <div class="m-symbol">{{ m }}</div>
                </div>
              </div>
            </div>
        </div>
      </aside>

      <main class="col col-orderbook">
        <div class="orderbook-card">
          <div class="ob-header-row">
            <div class="left-title">호가</div>
          </div>

          <div class="ob-content">
            <div v-if="loading" class="center-msg">호가 조회 중...</div>
            <div v-else-if="error" class="center-msg">
              <div>{{ errorMessage }}</div>
              <button class="retry-btn" @click="fetchOrderbookFor(selected, { background: false, isInit: true })">재시도</button>
            </div>
            <div v-else-if="(selectedBook.asks.length === 0) && (selectedBook.bids.length === 0)" class="center-msg">현재 미체결 호가가 없습니다.</div>
            <div v-else>
              <!-- column headers -->
              <div class="ob-columns ob-columns-head">
                <div class="col-price">가격</div>
                <div class="col-qty">수량</div>
                <div class="col-cum">누적 수량</div>
              </div>

              <div class="ob-asks">
                <div v-for="(a, i) in selectedBook.asks" :key="'ask'+i" class="ob-row ask">
                  <div class="price">{{ formatNumber(a.price) }} KRW</div>
                  <div class="qty">{{ formatQuantity(a.qty) }}</div>
                  <div class="cum">{{ a.cum !== undefined ? formatQuantity(a.cum) : '' }}</div>
                </div>
              </div>

              <div class="spread-full">
                <div class="spread-pill">
                  스프레드: {{ (selectedBook.asks[0] && selectedBook.bids[0]) ? formatNumber(selectedBook.asks[0].price - selectedBook.bids[0].price) : '-' }} KRW
                </div>
              </div>

              <div class="ob-bids">
                <div v-for="(b, i) in selectedBook.bids" :key="'bid'+i" class="ob-row bid">
                  <div class="price">{{ formatNumber(b.price) }} KRW</div>
                  <div class="qty">{{ formatQuantity(b.qty) }}</div>
                  <div class="cum">{{ b.cum !== undefined ? formatQuantity(b.cum) : '' }}</div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </main>

      <!-- Recent trades panel removed -->
    </section>
  </div>
</template>

<style scoped>
.market-page {
  padding: 22px 28px;
}
.page-header h2 {
  margin: 0;
  font-size: 20px;
}
.desc {
  margin: 6px 0 10px 0;
  color: #9fb0c1;
}
.divider {
  border: 0;
  height: 1px;
  background: rgba(20, 40, 50, 0.4);
  margin-bottom: 12px;
}


/* replaced LIVE indicator with demo badge only */
.live-dot-small { display: none; }
.live-badge { display: none; }

.summary-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  background: #071822;
  border-radius: 10px;
  border: 1px solid #12323a;
}
.summary-left {
  display: flex;
  gap: 18px;
  align-items: center;
}
.symbol {
  font-weight: 800;
  font-size: 16px;
}
.price {
  font-size: 16px;
  color: #f3f7fc;
}
.pct {
  font-weight: 700;
}
.pct.up {
  color: #2ed39a;
}
.pct.down {
  color: #ff6b6b;
}
.demo-badge {
  background: #0f2a36;
  color: #9fb0c1;
  padding: 6px 10px;
  border-radius: 8px;
}
.api-badge {
  background: #12323a;
  color: #bfe8db;
  padding: 6px 10px;
  border-radius: 8px;
  margin-left: 8px;
  font-size: 12px;
}
/* states */
.api-badge.loading {
  background: #2f6fa6; /* blue/gray */
  color: #ffffff;
}
.api-badge.success {
  background: #2ed39a; /* green */
  color: #052216;
}
.api-badge.error {
  background: #cc4b4b; /* red */
  color: #ffffff;
}

.content {
  display: flex;
  gap: 20px;
  margin-top: 18px;
}
.col {
  display: flex;
  flex-direction: column;
}
.col-markets {
  width: 260px;
}

.left-card {
  background: #071a24;
  border: 1px solid #16323b;
  padding: 10px;
  border-radius: 10px;
  /* stretch to match other cards' height */
  flex: 1;
}
.left-card-header h4 {
  margin: 0 0 8px 0;
}
.col-orderbook {
  flex: 1;
  min-width: 640px;
}
/* .col-trades removed */

.markets-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
/* ensure markets list scrolls internally without growing the page */
.scrollable-markets {
  max-height: 100%;
  overflow-y: auto;
}
.market-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 8px;
  color: #b6d4df;
  cursor: pointer;
}
.market-row.active {
  background: linear-gradient(180deg, rgba(13, 27, 42, 0.6), rgba(7, 18, 28, 0.6));
  border-radius: 8px;
  padding: 12px;
}
.mr-left .m-symbol {
  font-weight: 700;
}
.mr-right {
  display: flex;
  gap: 8px;
  align-items: center;
}
.m-price {
  color: #9fb0c1;
}
.m-change.up {
  color: #2ed39a;
}
.m-change.down {
  color: #ff6b6b;
}

.orderbook-card {
  background: #071a24;
  padding: 14px;
  border-radius: 12px;
  border: 1px solid #16323b;
  /* allow orderbook card to occupy available column height */
  flex: 1;
}
.ob-header-row {
  display: flex;
  justify-content: space-between;
  padding: 8px 12px;
  color: #9fb0c1;
  border-bottom: 1px solid rgba(23, 49, 65, 0.25);
  margin-bottom: 10px;
}
.th {
  font-size: 13px;
}
.ob-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px;
  border-radius: 8px;
}
.ask {
  background: linear-gradient(90deg, rgba(112, 28, 39, 0.12), rgba(112, 28, 39, 0.06));
  color: #ffb3b3;
  margin-bottom: 8px;
}
.bid {
  background: linear-gradient(90deg, rgba(28, 74, 52, 0.12), rgba(28, 74, 52, 0.06));
  color: #b7ffd9;
  margin-top: 8px;
}
.ob-row .price {
  text-align: left;
  width: 45%;
}
.ob-row .qty {
  text-align: center;
  width: 30%;
}
.ob-row .cum {
  text-align: right;
  width: 25%;
}

/* column header row for price/qty/cum */
.ob-columns {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 8px 12px;
  color: #9fb0c1;
  font-size: 13px;
  border-bottom: 1px solid rgba(23, 49, 65, 0.25);
  margin-bottom: 8px;
}
.ob-columns .col-price { width: 45%; text-align: left; }
.ob-columns .col-qty { width: 30%; text-align: center; }
.ob-columns .col-cum { width: 25%; text-align: right; }

.spread-full {
  display: flex;
  justify-content: center;
  margin: 10px 0;
}
.spread-pill {
  background: #0e2b32;
  padding: 10px 14px;
  border-radius: 12px;
  border: 1px solid #18464d;
  color: #bfe8db;
  width: 100%;
  text-align: center;
}

.left-title {
  font-weight: 700;
  color: #dff6ec;
  margin-bottom: 8px;
}

.ai-note.yellow {
  background: rgba(255, 243, 205, 0.06);
  color: #ffd86b;
  padding: 8px;
  border-radius: 8px;
}

/* trades-list removed with recent trades panel */

.center-msg {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 24px;
  color: #9fb0c1;
  text-align: center;
}
.retry-btn {
  margin-top: 8px;
  padding: 6px 10px;
  border-radius: 6px;
  background: #0f4650;
  color: #bfe8db;
  border: 1px solid #18464d;
  cursor: pointer;
}

.divider-hr {
  height: 1px;
  background: rgba(23, 49, 65, 0.25);
  margin: 12px 0;
}
/* recent trades styles removed */

@media (max-width: 1100px) {
  .content {
    flex-direction: column;
  }
  .col-orderbook {
    min-width: unset;
  }
}
</style>
