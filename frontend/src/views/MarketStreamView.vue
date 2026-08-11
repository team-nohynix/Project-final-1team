<script setup lang="ts">
import { ref } from 'vue'

const collector = ref({
  name: '업비트 시세 수집기',
  markets: 20,
  lastMessage: '42ms ago',
  reconnectPolicy: '≤ 30 sec',
})

const metrics = ref([
  { title: 'UPBIT IN', value: '4,821 msg/s', desc: '실시간 ticker/trade', color: '#20c8e8' },
  { title: '캐시 적중률', value: '99.98%', desc: 'Redis latest price', color: '#2ed39a' },
  { title: '실시간 접속자', value: '5,000', desc: '평균 3개 마켓', color: '#3478f6' },
  { title: '시세 전달 p99', value: '218 ms', desc: '목표 ≤ 300ms', color: '#8b5cf6' },
])

const markets = ref([
  { market: 'BTC/KRW', price: 98420000, latency: '42ms', ws: 1842, warn: false },
  { market: 'ETH/KRW', price: 5120000, latency: '38ms', ws: 1228, warn: false },
  { market: 'XRP/KRW', price: 842, latency: '51ms', ws: 986, warn: false },
  { market: 'SOL/KRW', price: 214500, latency: '47ms', ws: 821, warn: false },
  { market: 'DOGE/KRW', price: 214, latency: '82ms', ws: 704, warn: true },
  { market: 'ADA/KRW', price: 738, latency: '44ms', ws: 631, warn: false },
])

const subscription = ref({
  clientId: 'client-ws-04D21',
  markets: [
    { name: 'BTC/KRW', color: '#3478f6' },
    { name: 'ETH/KRW', color: '#2ed39a' },
    { name: 'SOL/KRW', color: '#8b5cf6' },
  ],
  connectedSince: '18m 42s',
  messagesSent: 184221,
})

// 프론트엔드 모의 상태 — 실제 Upbit/Redis/WebSocket 연동 없음
const isConnected = ref(true)
const isSubscribed = ref(true)

const unsubscribe = () => {
  if (!isSubscribed.value) return
  isSubscribed.value = false
}

const disconnect = () => {
  if (!isConnected.value) return
  const ok = window.confirm('프론트엔드 모의 동작입니다. 연결을 종료 상태로 표시하시겠습니까?')
  if (!ok) return
  isConnected.value = false
}
</script>

<template>
  <div class="market-page">
    <header class="market-header">
      <h2 class="market-title">시세 전송 현황</h2>
      <p class="market-subtitle">업비트 수집·Redis 캐시·WebSocket 구독 관리</p>
      <hr class="market-divider" />
    </header>

    <div class="market-collector-card">
      <div class="market-collector-left">
        <div class="market-collector-name">{{ collector.name }}</div>
        <div class="market-collector-status">
          <span class="market-status-dot" :class="{ on: isConnected }"></span>
          <span class="market-status-text" :class="{ off: !isConnected }">{{ isConnected ? '연결됨' : '연결 끊김' }}</span>
          <span class="market-collector-meta">{{ collector.markets }} KRW markets · last message {{ collector.lastMessage }}</span>
        </div>
      </div>
      <div class="market-collector-right">
        <div class="market-reconnect-label">Reconnect policy</div>
        <div class="market-reconnect-value">{{ collector.reconnectPolicy }}</div>
      </div>
    </div>

    <div class="market-metrics-grid">
      <div v-for="(m, i) in metrics" :key="i" class="market-metric-card">
        <span class="market-metric-dot" :style="{ background: m.color }"></span>
        <div class="market-metric-title">{{ m.title }}</div>
        <div class="market-metric-value">{{ m.value }}</div>
        <div class="market-metric-desc">{{ m.desc }}</div>
      </div>
    </div>

    <div class="market-bottom-grid">
      <div class="market-list-card">
        <h4 class="market-card-title">마켓별 시세</h4>
        <div class="market-row market-row-head">
          <div>마켓</div>
          <div>현재가</div>
          <div>지연시간</div>
          <div>WS/s</div>
        </div>
        <div v-for="(m, i) in markets" :key="i" class="market-row">
          <div class="market-name">{{ m.market }}</div>
          <div class="market-price">{{ m.price.toLocaleString() }}</div>
          <div class="market-latency-badge" :class="{ warn: m.warn }">
            <span class="dot"></span>{{ m.latency }}
          </div>
          <div class="market-ws">{{ m.ws.toLocaleString() }} WS/s</div>
        </div>
      </div>

      <div class="market-sub-card">
        <h4 class="market-card-title">사용자 구독 현황</h4>
        <div class="market-client-id">{{ subscription.clientId }}</div>

        <div class="market-sub-label">구독 중인 마켓</div>
        <div class="market-market-pills">
          <span
            v-for="(mk, i) in subscription.markets"
            :key="i"
            class="market-pill"
            :style="{ color: mk.color, borderColor: mk.color }"
          >
            <span class="dot" :style="{ background: mk.color }"></span>{{ mk.name }}
          </span>
        </div>

        <div class="market-sub-row">
          <div class="market-sub-key">연결 상태</div>
          <div class="market-sub-val" :class="{ ok: isConnected }">
            {{ isConnected ? `ACTIVE · ${subscription.connectedSince}` : 'DISCONNECTED' }}
          </div>
        </div>
        <div class="market-sub-row">
          <div class="market-sub-key">전송 메시지</div>
          <div class="market-sub-val">{{ subscription.messagesSent.toLocaleString() }}</div>
        </div>

        <div class="market-actions">
          <button type="button" class="market-btn-dark" :disabled="!isSubscribed" @click="unsubscribe">
            구독 해제
          </button>
          <button type="button" class="market-btn-danger" :disabled="!isConnected" @click="disconnect">
            연결 종료
          </button>
        </div>

        <p class="market-mock-note">* 프론트엔드 모의 동작입니다. 실제 서버 연결에는 영향을 주지 않습니다.</p>

        <div class="market-fanout-card">
          <span class="market-status-dot" :class="{ on: isSubscribed }"></span>
          <div>
            <div class="market-fanout-title">{{ isSubscribed ? 'FAN-OUT OK' : 'FAN-OUT 중지' }}</div>
            <div class="market-fanout-sub">{{ isSubscribed ? '구독 마지막 전송 중' : '구독이 해제되었습니다' }}</div>
          </div>
        </div>
      </div>
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
.market-status-dot.on { background: #2ed39a }
.market-status-text { color: #2ed39a; font-weight: 700 }
.market-status-text.off { color: #ff5c5c }
.market-collector-meta { color: #9fb0c2 }

.market-collector-right { text-align: right }
.market-reconnect-label { color: #9fb0c2; font-size: 12px }
.market-reconnect-value { color: #2ed39a; font-weight: 700; margin-top: 4px; font-size: 14px }

.market-metrics-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
.market-metric-card {
  position: relative;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 18px;
  min-height: 105px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
}
.market-metric-dot { position: absolute; top: 16px; right: 16px; width: 10px; height: 10px; border-radius: 50% }
.market-metric-title { color: #fff; font-size: 12px }
.market-metric-value { color: #fff; font-weight: 800; font-size: 24px; margin-top: 10px }
.market-metric-desc { color: #fff; font-size: 12px; margin-top: 8px }

.market-bottom-grid {
  display: grid;
  grid-template-columns: 60% 40%;
  gap: 16px;
  align-items: stretch;
}
.market-list-card,
.market-sub-card {
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

.market-client-id {
  font-weight: 700;
  font-size: 14px;
  background: #071826;
  border: 1px solid #172a3e;
  border-radius: 8px;
  padding: 10px 12px;
  margin-top: 4px;
}
.market-sub-label { color: #9fb0c2; font-size: 12px; margin-top: 16px }
.market-market-pills { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px }
.market-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border: 1px solid;
  border-radius: 20px;
  font-size: 12px;
  font-weight: 700;
  background: #071826;
}
.market-pill .dot { width: 6px; height: 6px; border-radius: 50% }

.market-sub-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 0;
  margin-top: 6px;
  border-bottom: 1px solid #0b2534;
}
.market-sub-key { color: #9fb0c2; font-size: 13px }
.market-sub-val { font-weight: 700; font-size: 13px }
.market-sub-val.ok { color: #2ed39a }

.market-actions { display: flex; gap: 10px; margin-top: 16px }
.market-btn-dark,
.market-btn-danger {
  flex: 1;
  height: 40px;
  box-sizing: border-box;
  appearance: none;
  -webkit-appearance: none;
  -moz-appearance: none;
  border: 0;
  border-radius: 8px;
  font-weight: 700;
  font-size: 13px;
  cursor: pointer;
  color: #fff;
}
.market-btn-dark { background: #11243a }
.market-btn-dark:hover:not(:disabled) { background: #16304d }
.market-btn-danger { background: #e5484d }
.market-btn-danger:hover:not(:disabled) { background: #c93e42 }
.market-btn-dark:disabled,
.market-btn-danger:disabled { opacity: 0.5; cursor: not-allowed }

.market-mock-note { color: #7d8fa3; font-size: 11px; margin-top: 10px; line-height: 1.5 }

.market-fanout-card {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 16px;
  padding: 12px 14px;
  background: #071826;
  border: 1px solid #172a3e;
  border-radius: 10px;
}
.market-fanout-title { font-weight: 700; font-size: 13px; color: #2ed39a }
.market-fanout-sub { color: #9fb0c2; font-size: 12px; margin-top: 2px }

@media (max-width: 900px) {
  .market-metrics-grid { grid-template-columns: repeat(2, minmax(0, 1fr)) }
  .market-bottom-grid { grid-template-columns: 1fr }
  .market-row { grid-template-columns: 1fr 1fr; row-gap: 4px }
  .market-row-head { display: none }
}
</style>
