<script setup lang="ts">
import { ref } from 'vue'

const experiment = ref({ id: 'TR-20260729-07', name: 'burst-market-20-v3', rate: '30K/s', duration: '10 min', markets: 20, status: 'pass' })

const summary = ref({ total: 18000000, accepted: 5988240, rejected: 12011760, errors: 0 })

const nfrs = ref([
  { name: '접수 TPS ≥ 10K', value: '29,968/s', result: 'pass' },
  { name: 'E2E p99 ≤ 500ms', value: '486ms', result: 'pass' },
  { name: 'Scale-out ≤ 120s', value: '84 sec', result: 'pass' },
  { name: '순서·유실·중복', value: '0 cases', result: 'pass' },
])

const faults = ref([
  { name: '매칭 엔진 Pod kill', time: '38s', result: 'Recovered' },
  { name: 'Kafka broker stop', time: '0 loss', result: 'Recovered' },
  { name: 'Worker node drain', time: '51s', result: 'Recovered' },
])

const traceId = ref('ORD-20260729-BTC-8F21A0D9')
const traceResult = ref([
  { step: 1, name: 'API 접수', time: '8ms', color: '#3478f6' },
  { step: 2, name: 'Kafka 주문 적재', time: '21ms', color: '#20c8e8' },
  { step: 3, name: '매칭 완료', time: '184ms', color: '#2ed39a' },
  { step: 4, name: '체결 결과 발행', time: '36ms', color: '#8b5cf6' },
  { step: 5, name: 'PostgreSQL', time: '72ms', color: '#ff9f43' },
])

const doSearch = () => {
  if (!traceId.value) return
  // mock: would call API later
}
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
        <div class="exp-id">Experiment #{{ experiment.id }}</div>
        <div class="exp-desc">{{ experiment.name }} · {{ experiment.rate }} · {{ experiment.duration }} · {{ experiment.markets }} markets</div>
      </div>
      <div class="exp-right"><div class="badge pass">통과</div></div>
    </div>

    <div class="summary-grid">
      <div class="summary-card">
        <span class="dot" style="background:#3478f6"></span>
        <div class="title">전체 입력 주문</div>
        <div class="value">{{ summary.total.toLocaleString() }}</div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#2ed39a"></span>
        <div class="title">접수 주문</div>
        <div class="value">{{ summary.accepted.toLocaleString() }}</div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#ff9f43"></span>
        <div class="title">과부하 거절(429)</div>
        <div class="value">{{ summary.rejected.toLocaleString() }}</div>
      </div>
      <div class="summary-card">
        <span class="dot" style="background:#ff4b4b"></span>
        <div class="title">데이터 오류</div>
        <div class="value">{{ summary.errors }}</div>
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
          <div v-for="(f,i) in faults" :key="i" class="fault-row">
            <div class="f-name">{{ f.name }}</div>
            <div class="f-time">{{ f.time }}</div>
            <span class="badge recovered">{{ f.result }}</span>
          </div>
        </div>
      </div>
    </div>

    <div class="trace-card">
      <h4 class="card-title">주문 처리 구간 추적</h4>
      <div class="trace-controls">
        <input v-model="traceId" type="text" class="trace-input" />
        <button type="button" class="btn-primary" @click="doSearch">Search</button>
      </div>

      <div class="timeline">
        <div v-for="(t,i) in traceResult" :key="i" class="tl-step">
          <div class="circle" :style="{ background: t.color }">{{ t.step }}</div>
          <div class="tl-name">{{ t.name }}</div>
          <div class="tl-time">{{ t.time }}</div>
        </div>
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
</style>
