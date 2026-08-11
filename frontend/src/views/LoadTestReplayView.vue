<script setup lang="ts">
import { ref } from 'vue'

const recordFile = ref('burst-market-20-v3.jsonl')
const throughput = ref('30000')
const speed = ref('100×')
const market = ref('20 KRW markets')
const pods = ref('4 pods')

const speedOptions = ['1×', '10×', '50×', '100×']
const marketOptions = ['5 KRW markets', '10 KRW markets', '20 KRW markets']
const podOptions = ['1 pod', '2 pods', '4 pods', '8 pods']

const precheckMessage = ref('')
const startMessage = ref('')
const errorMessage = ref('')

const graphBars = [8, 12, 18, 22, 26, 24, 30, 28, 32, 34, 30, 26, 24, 20, 18, 22, 26, 28, 30, 34]

const botRatios = [
  { name: '마켓메이커', value: 52, color: '#3478f6' },
  { name: '모멘텀 추종', value: 14, color: '#20c8e8' },
  { name: '평균회귀', value: 13, color: '#2ed39a' },
  { name: '노이즈', value: 16, color: '#8b5cf6' },
  { name: '대량 주문자', value: 5, color: '#fbbf24' },
]

const validate = () => {
  errorMessage.value = ''
  if (!recordFile.value) return '주문 기록 파일을 입력해주세요'
  const t = Number(throughput.value)
  if (!t || Number.isNaN(t) || t <= 0) return '목표 주문 처리량을 올바르게 설정해주세요'
  return ''
}

const onPrecheck = () => {
  startMessage.value = ''
  const err = validate()
  if (err) {
    errorMessage.value = err
    precheckMessage.value = ''
    return
  }
  errorMessage.value = ''
  precheckMessage.value = '사전 점검 완료'
}

const onStart = () => {
  errorMessage.value = ''
  precheckMessage.value = ''
  const err = validate()
  if (err) {
    errorMessage.value = err
    startMessage.value = ''
    return
  }
  startMessage.value = '더미 재생 요청이 생성되었습니다'
}
</script>

<template>
  <div>
    <header class="page-header">
      <h2>부하 테스트 제어</h2>
      <p class="subtitle">AI 트레이더 주문 기록과 동일 패턴 재생 설정</p>
      <hr />
    </header>

    <div class="content-grid">
      <section class="panel left-panel">
        <h3 class="panel-title">주문 재생 설정</h3>
        <p class="panel-sub">성능 비교를 위한 결정적 주문 재생</p>

        <div class="form-field">
          <label>주문 기록 파일</label>
          <input v-model="recordFile" type="text" />
        </div>

        <div class="form-field">
          <label>목표 주문 처리량</label>
          <input v-model.number="throughput" type="number" />
        </div>

        <div class="form-field">
          <label>재생 배속</label>
          <select v-model="speed">
            <option v-for="s in speedOptions" :key="s">{{ s }}</option>
          </select>
        </div>

        <div class="form-field">
          <label>대상 마켓</label>
          <select v-model="market">
            <option v-for="m in marketOptions" :key="m">{{ m }}</option>
          </select>
        </div>

        <div class="form-field">
          <label>분산 재생기</label>
          <select v-model="pods">
            <option v-for="p in podOptions" :key="p">{{ p }}</option>
          </select>
        </div>

        <div class="actions">
          <button class="btn-primary" @click="onStart">재생 시작</button>
          <button class="btn-dark" @click="onPrecheck">사전 점검</button>
        </div>

        <div class="messages">
          <p v-if="errorMessage" class="error">{{ errorMessage }}</p>
          <p v-if="precheckMessage" class="success">{{ precheckMessage }}</p>
          <p v-if="startMessage" class="info">{{ startMessage }}</p>
        </div>
      </section>

      <aside class="panel right-panel">
        <h3 class="panel-title">부하 시나리오 미리보기</h3>
        <p class="panel-sub">예상 부하 분포와 검증 기준</p>

        <div class="bar-chart">
          <div class="bars">
            <div
              v-for="(v, idx) in graphBars"
              :key="idx"
              class="bar"
              :class="{ teal: idx >= graphBars.length - 4 }"
              :style="{ height: v * 3 + 'px' }"
            ></div>
          </div>
        </div>

        <h4 class="section-title">봇별 주문 비율</h4>
        <div class="ratios">
          <div v-for="(b, i) in botRatios" :key="i" class="ratio-row">
            <div class="ratio-label">{{ b.name }}</div>
            <div class="ratio-bar">
              <div class="ratio-fill" :style="{ width: b.value + '%', background: b.color }"></div>
            </div>
            <div class="ratio-value">{{ b.value }}%</div>
          </div>
        </div>

        <div class="status-box">
          <div class="status-left">
            <span class="status-dot"></span>
            <span>준비 완료</span>
          </div>
          <div class="status-right">입력 파일 검증 완료 · 1,800,000 orders</div>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.page-header h2 {
  margin: 0 0 6px 0;
  font-size: 22px;
  color: #ffffff;
}
.subtitle {
  margin: 0 0 14px 0;
  color: #9fb0c2;
}
.page-header hr {
  border: 0;
  height: 1px;
  background: #0f2636;
  margin-bottom: 20px;
}

.content-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 24px;
}

.panel {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
}

.panel-title {
  font-size: 16px;
  margin: 0 0 6px 0;
}
.panel-sub {
  color: #9fb0c2;
  margin: 0 0 16px 0;
}

.form-field {
  margin-bottom: 12px;
}
.form-field label {
  display: block;
  font-size: 12px;
  color: #9fb0c2;
  margin-bottom: 8px;
}
.form-field input,
.form-field select {
  width: 100%;
  padding: 12px 14px;
  background: #072037;
  border: 1px solid #163247;
  color: #e6eef8;
  border-radius: 8px;
  outline: none;
}
.form-field input[type='number']::-webkit-outer-spin-button,
.form-field input[type='number']::-webkit-inner-spin-button {
  -webkit-appearance: none;
  margin: 0;
}

.actions {
  display: flex;
  gap: 12px;
  margin-top: 16px;
}
.btn-primary {
  flex: 1.4;
  background: #3f86ff;
  color: #fff;
  border: 0;
  padding: 12px 18px;
  border-radius: 10px;
  cursor: pointer;
}
.btn-dark {
  flex: 0.8;
  background: #18324a;
  color: #e6eef8;
  border: 0;
  padding: 12px 18px;
  border-radius: 10px;
  cursor: pointer;
}

.messages {
  margin-top: 12px;
}
.messages .error {
  color: #ff6b6b;
}
.messages .success {
  color: #2ed39a;
}
.messages .info {
  color: #cfe6ff;
}

.right-panel {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
}

.bar-chart .bars {
  display: flex;
  align-items: flex-end;
  gap: 6px;
  height: 140px;
  padding: 12px 6px;
  background: transparent;
}
.bar-chart .bar {
  width: 14px;
  background: #8b5cf6;
  border-radius: 6px 6px 0 0;
}
.bar-chart .bar.teal {
  background: #20c8e8;
}

.section-title {
  margin: 16px 0 8px 0;
  color: #d7e8fb;
}
.ratios {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.ratio-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.ratio-label {
  width: 120px;
  color: #c6d6e6;
}
.ratio-bar {
  flex: 1;
  height: 12px;
  background: #072b45;
  border-radius: 8px;
  overflow: hidden;
}
.ratio-fill {
  height: 100%;
}
.ratio-value {
  width: 48px;
  text-align: right;
  color: #c6d6e6;
}

.status-box {
  margin-top: 18px;
  background: #081826;
  border-radius: 10px;
  padding: 12px 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: #bcd8e9;
}
.status-dot {
  width: 10px;
  height: 10px;
  background: #2ed39a;
  border-radius: 50%;
  margin-right: 8px;
}
.status-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

@media (max-width: 900px) {
  .content-grid {
    grid-template-columns: 1fr;
  }
  body {
    min-width: 0;
  }
}
</style>
