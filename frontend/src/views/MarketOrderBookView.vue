<script setup>
import { ref, computed, watch } from 'vue'

// NOTE: All data in this view is dummy/mock data for UI purposes only.
const markets = ref([
  { symbol: 'BTC/KRW', price: 98420000, change: 2.14, short: '98.42M' },
  { symbol: 'ETH/KRW', price: 3124000, change: 1.08, short: '5.12M' },
  { symbol: 'XRP/KRW', price: 800, change: -0.42, short: '842' },
  { symbol: 'SOL/KRW', price: 214500, change: 3.26, short: '214,500' },
  { symbol: 'DOGE/KRW', price: 214, change: -1.12, short: '214' },
  { symbol: 'ADA/KRW', price: 738, change: 0.63, short: '738' },
])

const selected = ref(markets.value[0].symbol)

const orderbooks = {
  'BTC/KRW': {
    asks: [
      { price: 98460000, qty: 0.281, cum: 2.482 },
      { price: 98450000, qty: 0.912, cum: 2.201 },
      { price: 98440000, qty: 0.408, cum: 1.289 },
      { price: 98430000, qty: 0.881, cum: 0.881 },
    ],
    bids: [
      { price: 98420000, qty: 0.624, cum: 0.624 },
      { price: 98410000, qty: 0.993, cum: 1.617 },
      { price: 98400000, qty: 0.484, cum: 2.101 },
      { price: 98390000, qty: 1.22, cum: 3.321 },
    ],
  },
  'ETH/KRW': {
    asks: [
      { price: 3140000, qty: 1.2 },
      { price: 3130000, qty: 0.8 },
      { price: 3125000, qty: 0.5 },
      { price: 3124000, qty: 0.2 },
    ],
    bids: [
      { price: 3123000, qty: 0.6 },
      { price: 3120000, qty: 2 },
      { price: 3115000, qty: 1.5 },
      { price: 3110000, qty: 0.8 },
    ],
  },
}

// Recent trades (display values formatted per mock)
const trades = ref([
  { price: 98420000, qty: 0.024, time: '14:02:31', side: 'buy' },
  { price: 98410000, qty: 0.112, time: '14:02:30', side: 'sell' },
  { price: 98420000, qty: 0.052, time: '14:02:29', side: 'buy' },
  { price: 98430000, qty: 0.008, time: '14:02:27', side: 'buy' },
])

const selectedBook = computed(() => {
  // For BTC/KRW use the mock orderbook above, otherwise fallback to available data
  return orderbooks[selected.value] || { asks: [], bids: [] }
})

const currentPrice = computed(() => {
  const m = markets.value.find((x) => x.symbol === selected.value)
  return m ? m.price : 0
})

const changePercent = computed(() => {
  const m = markets.value.find((x) => x.symbol === selected.value)
  return m ? m.change : 0
})

const selectMarket = (s) => {
  selected.value = s
}

// AI order flow state for right card
const aiSide = ref('buy') // initial selection: buy
const aiPrice = ref(currentPrice.value)
const aiQty = ref(0.125)
const statusMessage = ref('')

watch(selected, () => {
  // update default price when market changes
  aiPrice.value = currentPrice.value
})

const aiValid = computed(() => {
  return Number(aiPrice.value) > 0 && Number(aiQty.value) > 0
})

const toggleSide = (s) => {
  aiSide.value = s
}

const createAIOrder = () => {
  if (!aiValid.value) return
  statusMessage.value = '더미 AI 주문 생성 완료'
  setTimeout(() => {
    statusMessage.value = ''
  }, 3000)
}

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

const onPriceInput = (val) => {
  // accept numbers with commas
  const parsed = Number(String(val).replace(/,/g, ''))
  if (!isNaN(parsed)) aiPrice.value = parsed
}
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
        <div class="price">{{ currentPrice.toLocaleString() }} KRW</div>
        <div :class="['pct', changePercent >= 0 ? 'up' : 'down']">
          {{ (changePercent >= 0 ? '+' : '') + changePercent + '%' }}
        </div>
      </div>
      <div class="summary-right">
        <span class="live-dot-small"></span>
        <div class="live-badge">LIVE</div>
        <div class="demo-badge">데모 데이터</div>
      </div>
    </div>

    <section class="content">
      <aside class="col col-markets">
        <div class="left-card">
          <div class="left-card-header"><h4>마켓·호가창</h4></div>
          <div class="markets-list">
            <div
              v-for="m in markets"
              :key="m.symbol"
              :class="['market-row', { active: m.symbol === selected }]"
              @click="selectMarket(m.symbol)"
            >
              <div class="mr-left">
                <div class="m-symbol">{{ m.symbol }}</div>
              </div>
              <div class="mr-right">
                <div class="m-price">{{ m.short }}</div>
                <div :class="['m-change', m.change >= 0 ? 'up' : 'down']">
                  {{ (m.change >= 0 ? '+' : '') + m.change + '%' }}
                </div>
              </div>
            </div>
          </div>
        </div>
      </aside>

      <main class="col col-orderbook">
        <div class="orderbook-card">
          <div class="left-title">호가창</div>
          <div class="ob-header-row">
            <span class="th price">가격</span><span class="th qty">수량</span
            ><span class="th cum">누적</span>
          </div>

          <div class="asks">
            <div v-for="(a, i) in selectedBook.asks.slice(0, 4)" :key="'a' + i" class="ob-row ask">
              <div class="price">{{ a.price.toLocaleString() }}</div>
              <div class="qty">{{ a.qty }}</div>
              <div class="cum">
                {{
                  a.cum !== undefined
                    ? a.cum.toFixed(4)
                    : selectedBook.asks
                        .slice(0, i + 1)
                        .reduce((s, x) => s + x.qty, 0)
                        .toFixed(4)
                }}
              </div>
            </div>
          </div>

          <div class="spread-full">
            <div class="spread-pill">₩10,000 · 0.01%</div>
          </div>

          <div class="bids">
            <div v-for="(b, i) in selectedBook.bids.slice(0, 4)" :key="'b' + i" class="ob-row bid">
              <div class="price">{{ b.price.toLocaleString() }}</div>
              <div class="qty">{{ b.qty }}</div>
              <div class="cum">
                {{
                  b.cum !== undefined
                    ? b.cum.toFixed(4)
                    : selectedBook.bids
                        .slice(0, i + 1)
                        .reduce((s, x) => s + x.qty, 0)
                        .toFixed(4)
                }}
              </div>
            </div>
          </div>
        </div>
      </main>

      <aside class="col col-trades">
        <div class="right-card">
          <div class="right-card-header">
            <h4>최근 체결</h4>
          </div>

          <div class="trades-list">
            <div v-for="t in trades" :key="t.time + t.price" class="trade-item two-line">
              <div class="t-first-line">
                <div class="t-price" :class="t.side === 'buy' ? 'buy' : 'sell'">
                  {{ formatShort(t.price) }}
                </div>
              </div>
              <div class="t-second-line">
                <div class="t-qty">{{ t.qty }} BTC</div>
                <div class="t-time">{{ t.time }}</div>
              </div>
            </div>
          </div>

          <div class="divider-hr" />

          <div class="ai-order">
            <div class="ai-buttons">
              <button
                :class="['side-btn', aiSide === 'buy' ? 'buy-active' : '']"
                @click="toggleSide('buy')"
              >
                매수
              </button>
              <button
                :class="['side-btn', aiSide === 'sell' ? 'sell-active' : '']"
                @click="toggleSide('sell')"
              >
                매도
              </button>
            </div>

            <label class="input-label">가격 (KRW)</label>
            <input
              class="ai-input"
              type="text"
              :value="formatNumber(aiPrice)"
              @input="onPriceInput($event.target.value)"
            />

            <label class="input-label">수량 (BTC)</label>
            <input class="ai-input" type="number" step="0.001" v-model.number="aiQty" />

            <button class="ai-create" :disabled="!aiValid" @click="createAIOrder">
              AI 주문 유형 생성
            </button>

            <div class="ai-note yellow">TRUSS 내부 모의 주문 · 실제 자산 거래 없음</div>

            <div v-if="statusMessage" class="status-msg">{{ statusMessage }}</div>
          </div>
        </div>
      </aside>
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

.live-dot-small {
  display: inline-block;
  width: 8px;
  height: 8px;
  background: #ff4d4f;
  border-radius: 50%;
  margin-right: 8px;
}
.live-badge {
  display: inline-block;
  color: #ffb3b3;
  font-weight: 700;
  margin-right: 10px;
}

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
}
.left-card-header h4 {
  margin: 0 0 8px 0;
}
.col-orderbook {
  flex: 1;
  min-width: 640px;
}
.col-trades {
  width: 300px;
}

.markets-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
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

.trade-item.two-line {
  display: block;
  padding: 8px 6px;
  border-bottom: 1px solid rgba(23, 49, 65, 0.25);
}
.t-first-line {
  display: flex;
  justify-content: flex-start;
  align-items: center;
}
.t-second-line {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 6px;
}
.ai-note.yellow {
  background: rgba(255, 243, 205, 0.06);
  color: #ffd86b;
  padding: 8px;
  border-radius: 8px;
}

.trades-list {
  background: #071a24;
  padding: 12px;
  border-radius: 12px;
  border: 1px solid #16323b;
}
.trade-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 6px;
  border-bottom: 1px solid rgba(23, 49, 65, 0.25);
}
.t-left {
  display: flex;
  gap: 12px;
  align-items: center;
}
.t-price.buy {
  color: #2ed39a;
}
.t-price.sell {
  color: #ff6b6b;
}
.t-qty {
  color: #9fb0c1;
}
.t-time {
  color: #6f98a6;
  font-size: 12px;
}

.right-card {
  background: #071a24;
  border: 1px solid #16323b;
  border-radius: 12px;
  padding: 12px;
  height: 100%;
}
.right-card-header {
  margin-bottom: 8px;
}
.divider-hr {
  height: 1px;
  background: rgba(23, 49, 65, 0.25);
  margin: 12px 0;
}
.ai-order {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ai-buttons {
  display: flex;
  gap: 8px;
}
.side-btn {
  flex: 1;
  padding: 10px 12px;
  border-radius: 8px;
  background: #0b2530;
  color: #cfe6ef;
  border: 0;
  cursor: pointer;
}
.buy-active {
  background: #bff5d9;
  color: #0b2b20;
  font-weight: 700;
}
.sell-active {
  background: #ff6b6b;
  color: #ffffff;
  font-weight: 700;
}
.ai-input {
  padding: 10px;
  border-radius: 8px;
  background: #062028;
  border: 1px solid #12323a;
  color: #f3f7fc;
}
.input-label {
  color: #9fb0c1;
  font-size: 12px;
}
.ai-create {
  width: 100%;
  padding: 12px;
  border-radius: 10px;
  background: #2f8bff;
  color: white;
  border: 0;
  cursor: pointer;
  font-weight: 700;
}
.ai-create:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.ai-create:hover:not(:disabled) {
  filter: brightness(1.05);
}
.ai-note {
  font-size: 12px;
  color: #9fb0c1;
  margin-top: 6px;
}
.status-msg {
  margin-top: 8px;
  color: #bfe8db;
  font-weight: 700;
}

@media (max-width: 1100px) {
  .content {
    flex-direction: column;
  }
  .col-orderbook {
    min-width: unset;
  }
}
</style>
