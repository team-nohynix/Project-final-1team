<script setup lang="ts">
import { ref } from 'vue'

const info = ref({ title: '통제된 장애 주입 시험', desc: '실험 환경에서만 장애를 주입하며 데이터 유실·중복·순서 역전을 함께 검사합니다.' })

const faults = ref([
  { id: 'pod', name: '매칭 엔진 Pod', action: 'pod kill', nfr: 'NFR-11 ≤ 60s', color: '#ff5c5c' },
  { id: 'node', name: 'Worker Node', action: 'node drain', nfr: 'NFR-12 no loss', color: '#ff9f43' },
  { id: 'broker', name: 'Kafka 브로커', action: 'broker stop', nfr: 'NFR-12 no loss', color: '#8b5cf6' },
])

const activeFault = ref(null as string | null)
const running = ref(false)
const progress = ref([
  { t: 0, name: 'Pod terminated', color: '#ff5c5c' },
  { t: 8, name: 'K8s reschedule', color: '#ff9f43' },
  { t: 21, name: 'Replay started', color: '#20c8e8' },
  { t: 38, name: 'Processing resumed', color: '#2ed39a' },
])

const recent = ref([
  { name: 'Pod kill', result: '38.2 sec', status: '통과' },
  { name: 'Node drain', result: '51.4 sec', status: '통과' },
  { name: 'Kafka broker stop', result: '0 loss', status: '통과' },
])

const inject = (f: any) => {
  if (running.value) return
  const ok = window.confirm('실제 장애를 주입하지 않습니다. 프론트엔드 모의 시험을 실행하시겠습니까?')
  if (!ok) return
  activeFault.value = f.id
  running.value = true
  // simulate timeline progression
  setTimeout(() => { running.value = false; recent.value.unshift({ name: f.name, result: '38.0 sec', status: '통과' }); if (recent.value.length>10) recent.value.pop() }, 2000)
}
</script>

<template>
  <div class="fault-page">
    <header class="page-header">
      <h2>장애 주입·복구</h2>
      <p class="subtitle">Pod·Worker Node·Kafka 브로커 장애 주입과 복구 시간 검증</p>
      <hr />
    </header>

    <div class="fault-info-card">
      <div>
        <div class="fault-info-title">{{ info.title }}</div>
        <div class="fault-info-desc">{{ info.desc }}</div>
      </div>
      <div class="fault-badge-safe"><span class="dot"></span>SAFE MODE</div>
    </div>

    <div class="fault-grid">
      <div v-for="(f,i) in faults" :key="i" class="fault-card">
        <div class="fault-card-head">
          <span class="fault-dot" :style="{ background: f.color }"></span>
          <span class="fault-name">{{ f.name }}</span>
        </div>
        <div class="fault-meta">{{ f.action }} · {{ f.nfr }}</div>
        <button
          type="button"
          class="fault-inject-btn"
          :style="{ color: f.color }"
          :disabled="running"
          @click="inject(f)"
        >
          Inject
        </button>
      </div>
    </div>

    <div class="fault-middle">
      <div class="fault-timeline-card">
        <h4 class="card-title">복구 진행 과정</h4>
        <div class="fault-timeline">
          <div v-for="(p,i) in progress" :key="i" class="fault-tl-step">
            <div class="fault-tl-circle" :style="{ background: p.color }">{{ i + 1 }}</div>
            <div class="fault-tl-time">{{ p.t }}s</div>
            <div class="fault-tl-name">{{ p.name }}</div>
          </div>
        </div>
      </div>

      <div class="fault-check-card">
        <h4 class="card-title">데이터 정합성 검사</h4>
        <div class="fault-check-list">
          <div class="fault-check-row"><div class="fault-check-label">주문 유실</div><div class="fault-check-val ok">0</div></div>
          <div class="fault-check-row"><div class="fault-check-label">중복 체결</div><div class="fault-check-val ok">0</div></div>
          <div class="fault-check-row"><div class="fault-check-label">순서 역전</div><div class="fault-check-val ok">0</div></div>
          <div class="fault-check-row"><div class="fault-check-label">매수·매도 총량 불일치</div><div class="fault-check-val ok">0</div></div>
        </div>
      </div>
    </div>

    <div class="fault-recent-card">
      <h4 class="card-title">최근 장애 시험 결과</h4>
      <div v-for="(r,i) in recent" :key="i" class="fault-recent-row">
        <div class="fault-recent-name">{{ r.name }}</div>
        <div class="fault-recent-res">{{ r.result }}</div>
        <div class="fault-recent-badge"><span class="dot"></span>{{ r.status }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.fault-page {
  width: 100%;
  max-width: none;
  min-width: 0;
  box-sizing: border-box;
  padding-bottom: 32px;
}
.fault-page > * + * {
  margin-top: 16px;
}

.page-header h2 { margin: 0 0 6px }
.page-header .subtitle { color: #9fb0c2; font-size: 13px; margin: 0 }
.page-header hr {
  border: 0;
  height: 1px;
  background: #1b2e46;
  margin: 12px 0 0;
}

.card-title { margin: 0 0 14px; font-weight: 700; font-size: 15px }

/* 통제된 장애 주입 시험 카드 */
.fault-info-card {
  width: 100%;
  min-height: 90px;
  box-sizing: border-box;
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
}
.fault-info-title { font-weight: 700; font-size: 15px }
.fault-info-desc { color: #9fb0c2; margin-top: 6px; font-size: 13px }

.fault-badge-safe {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 12px;
  box-sizing: border-box;
  background: #2b1508;
  color: #ffb86b;
  border-radius: 20px;
  font-weight: 700;
  font-size: 12px;
}
.fault-badge-safe .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #ff9f43;
}

/* 장애 유형 카드 3개 */
.fault-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}
.fault-card {
  position: relative;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
  min-height: 125px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
}
.fault-card-head { display: flex; align-items: center; gap: 8px }
.fault-dot { width: 10px; height: 10px; border-radius: 50%; flex: 0 0 auto }
.fault-name { font-weight: 700; font-size: 14px }
.fault-meta { color: #9fb0c2; font-size: 13px; margin-top: 8px; line-height: 1.6 }

.fault-inject-btn {
  align-self: flex-end;
  margin-top: auto;
  width: 110px;
  height: 38px;
  box-sizing: border-box;
  appearance: none;
  -webkit-appearance: none;
  -moz-appearance: none;
  border: 0;
  border-radius: 7px;
  background: #0d2338;
  font-weight: 700;
  font-size: 13px;
  cursor: pointer;
}
.fault-inject-btn:hover:not(:disabled) { background: #123253 }
.fault-inject-btn:disabled { opacity: 0.5; cursor: not-allowed }

/* 중간 영역 */
.fault-middle {
  display: grid;
  grid-template-columns: 65% 35%;
  gap: 16px;
  align-items: stretch;
}
.fault-timeline-card,
.fault-check-card {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
  min-height: 230px;
  box-sizing: border-box;
  display: flex;
  flex-direction: column;
}

.fault-timeline {
  flex: 1;
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  align-items: center;
  align-content: center;
}
.fault-tl-step {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 0 6px;
}
.fault-tl-step:not(:last-child)::after {
  content: '';
  position: absolute;
  top: 15px;
  left: 50%;
  width: 100%;
  height: 2px;
  background: #172a3e;
  z-index: 0;
}
.fault-tl-circle {
  position: relative;
  z-index: 1;
  width: 30px;
  height: 30px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  font-size: 12px;
  color: #fff;
  margin-bottom: 8px;
}
.fault-tl-time { font-weight: 700; font-size: 13px }
.fault-tl-name { color: #9fb0c2; font-size: 12px; margin-top: 4px }

.fault-check-list {
  flex: 1;
  display: flex;
  flex-direction: column;
  justify-content: center;
}
.fault-check-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 14px 0;
}
.fault-check-label { color: #9fb0c2; font-size: 14px }
.fault-check-val.ok { color: #2ed39a; font-weight: 700; font-size: 15px }

/* 최근 장애 시험 결과 */
.fault-recent-card {
  width: 100%;
  min-height: 150px;
  box-sizing: border-box;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
}
.fault-recent-row {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid #0b2534;
}
.fault-recent-row:last-child { border-bottom: 0 }
.fault-recent-name { font-weight: 600; font-size: 14px }
.fault-recent-res { color: #9fb0c2; font-size: 14px; text-align: center }
.fault-recent-badge {
  justify-self: end;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: #072a1a;
  color: #2ed39a;
  padding: 4px 12px;
  border-radius: 20px;
  font-weight: 700;
  font-size: 12px;
}
.fault-recent-badge .dot { width: 6px; height: 6px; border-radius: 50%; background: #2ed39a }

@media (max-width: 900px) {
  .fault-grid { grid-template-columns: 1fr }
  .fault-middle { grid-template-columns: 1fr }
  .fault-timeline { grid-template-columns: 1fr; row-gap: 16px }
  .fault-tl-step {
    flex-direction: row;
    align-items: center;
    justify-content: flex-start;
    text-align: left;
    gap: 12px;
    padding: 0;
  }
  .fault-tl-step:not(:last-child)::after { display: none }
  .fault-tl-circle { margin-bottom: 0 }
  .fault-recent-row { grid-template-columns: 1fr; row-gap: 4px }
  .fault-recent-badge { justify-self: start }
}
</style>
