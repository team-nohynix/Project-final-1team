<script setup lang="ts">
import { ref, computed } from 'vue'

const metrics = ref([
  { title: '현재 투입량', value: '30.0K/s', desc: '부하 생성기 입력', color: '#3478f6' },
  { title: '정상 접수 TPS', value: '9.98K/s', desc: '처리 목표 10K/s', color: '#2ed39a' },
  { title: '과부하 거절(429)', value: '20.0K/s', desc: '초과 요청 즉시 거절', color: '#ff9f43' },
  { title: '매칭 Pod', value: '8', desc: 'KEDA 4 → 8', color: '#8b5cf6' },
])

// mock time-series data (25 points)
// single-bar chart data: height in percent and color category
const chartBars = ref(
  Array.from({ length: 25 }).map((_, i) => ({
    height: Math.round(20 + Math.random() * 80),
    color: i > 18 ? 'cyan' : 'blue',
  })),
)

const podList = ref([
  { name: 'engine-01', markets: 'BTC · ETH', status: 'running' },
  { name: 'engine-02', markets: 'XRP · SOL', status: 'running' },
  { name: 'engine-03', markets: 'ADA · DOGE', status: 'running' },
  { name: 'engine-04', markets: 'AVAX · LINK', status: 'starting' },
])

const integrity = ref({ reorder: 0, lost: 0, dup: 0, mismatch: 0 })
</script>

<template>
  <div class="monitoring-page">
    <header class="page-header">
      <h2>실시간 성능 모니터링</h2>
      <p class="subtitle">부하·지연·오토스케일링을 동일 시각축으로 관찰</p>
      <hr />
    </header>

    <div class="metrics-row">
      <div v-for="(m, i) in metrics" :key="i" class="metric-card-small">
        <div class="metric-top">
          <div class="metric-title">{{ m.title }}</div>
          <div class="metric-value" :style="{ color: m.color }">{{ m.value }}</div>
        </div>
        <div class="metric-sub">{{ m.desc }}</div>
      </div>
    </div>

    <div class="graph-area">
      <div class="large-graph">
        <div class="graph-top">
          <div>
            <div class="graph-title">입력 주문 / 정상 접수 / 429 거절 / 처리 대기 주문 / Pod 수</div>
            <div class="graph-sub">Prometheus 15초 간격 · Grafana 동일 시각축 · KEDA 120초 내 Scale-out 검증</div>
          </div>
          <div class="live-badge"><span class="live-dot"></span> LIVE</div>
        </div>

        <div class="chart-area">
          <div class="chart-bars">
            <div
              v-for="(b, idx) in chartBars"
              :key="idx"
              class="chart-bar"
              :class="b.color"
              :style="{ height: b.height + '%' }"
            ></div>
          </div>
        </div>
      </div>
    </div>

    <div class="bottom-cards">
      <div class="left-card">
        <h4 class="card-title">매칭 엔진 Pod</h4>
        <div class="pod-list">
          <div v-for="(p, i) in podList" :key="i" class="pod-row">
            <div class="pod-name">{{ p.name }}</div>
            <div class="pod-markets">{{ p.markets }}</div>
            <div class="pod-status" :class="{ running: p.status === 'running', starting: p.status === 'starting' }">
              {{ p.status === 'running' ? '실행 중' : '시작 중' }}
            </div>
          </div>
        </div>
      </div>

      <div class="right-card">
        <h4 class="card-title">데이터 정합성</h4>
        <div class="integrity">
          <div class="row"><div class="label">순서 역전:</div><div class="value ok">{{ integrity.reorder }}</div></div>
          <div class="row"><div class="label">주문 유실:</div><div class="value ok">{{ integrity.lost }}</div></div>
          <div class="row"><div class="label">중복 체결:</div><div class="value ok">{{ integrity.dup }}</div></div>
          <div class="row"><div class="label">매수·매도 불일치:</div><div class="value ok">{{ integrity.mismatch }}</div></div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.metrics-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
  margin-bottom: 16px;
}
.metric-card-small {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 10px;
  padding: 18px 20px; /* increased padding */
  height: 116px; /* target height */
  position: relative;
  display: flex;
  flex-direction: column;
  justify-content: flex-start;
}
.metric-top { display:flex; align-items:flex-start }
.metric-title { color:#9fb0c2; font-size:13px }
.metric-value { font-weight:800; font-size:22px; margin-top:8px }
.metric-sub { color:#9fb0c2; margin-top:8px; font-size:13px }
.metric-dot { position:absolute; right:14px; top:14px; width:12px; height:12px; border-radius:50% }

.graph-area { margin-top: 12px }
.large-graph { margin-top:12px; background:#071826; border:1px solid #163247; padding:18px; border-radius:10px }
.graph-top { display:flex; justify-content:space-between; align-items:flex-start }
.graph-title { font-weight:700 }
.graph-sub { color:#9fb0c2; font-size:13px; margin-top:6px }
.live-badge { display:flex; align-items:center; gap:8px; background:transparent; padding:6px 10px; border-radius:20px; color:#ffdddd; font-weight:700 }
.live-dot { width:10px;height:10px;background:#ff4b4b;border-radius:50%; box-shadow:0 0 6px rgba(255,75,75,0.6) }

.chart-area { width:100%; height:250px; padding:24px; box-sizing:border-box }
.chart-bars { display:flex; align-items:flex-end; justify-content:space-between; gap:6px; height:100%; width:100%; overflow:hidden }
.chart-bars { padding: 0 8px; }
.chart-bar { width:10px; border-radius:6px 6px 2px 2px; transition:height 180ms ease; flex: 0 0 auto }
.chart-bar.blue { background: linear-gradient(180deg,#3478f6,#1f5fb8) }
.chart-bar.cyan { background: linear-gradient(180deg,#20c8e8,#138a9f) }

.bottom-cards { display:grid; grid-template-columns: 1fr 320px; gap:12px; margin-top:12px }
.bottom-cards { display:grid; grid-template-columns: 60% 40%; gap:12px; margin-top:12px; align-items:stretch }
.left-card, .right-card { background:#0d1b2a; border:1px solid #172a3e; border-radius:10px; padding:18px; display:flex; flex-direction:column }
.card-title { margin:0 0 12px 0; font-weight:700 }
.pod-list { display:flex; flex-direction:column; gap:8px }
.pod-row { display:flex; align-items:center; gap:12px }
.pod-name { width:140px; font-weight:700 }
.pod-markets { color:#9fb0c2 }
.pod-status { margin-left:auto; padding:6px 12px; border-radius:999px; font-weight:700 }
.pod-status.running { background:#072a1a; color:#2ed39a; border:1px solid #123d2a }
.pod-status.starting { background:#2b1508; color:#ff9f43; border:1px solid #4a2a12 }

.integrity .row { display:flex; justify-content:space-between; padding:10px 0 }
.integrity .label { color:#9fb0c2 }
.integrity .value.ok { color:#2ed39a; font-weight:700 }

/* Header tweaks specific to this view */
.page-header h2 { margin-bottom:6px }
.page-header hr { border: 0; height: 1px; background: #071826; margin-bottom: 16px }
.subtitle { color: #9fb0c2 }

@media (max-width: 900px) {
  .metrics-row { grid-template-columns: repeat(2, 1fr) }
  .bottom-cards { grid-template-columns: 1fr }
}
</style>
