<script setup lang="ts">
import { ref } from 'vue'

// user-selectable run date (KST day)
const selectedDate = ref('') // YYYY-MM-DD

// target speed multiplier (numeric)
const speed = ref(100)
const speedOptions = [1, 10, 50, 100]

// shardCount replaces pod selection: number of replay shards (1..20)
const shardCount = ref(1)

const precheckMessage = ref('')
const startMessage = ref('')
const errorMessage = ref('')

const graphBars = ref([])

const validate = () => {
  errorMessage.value = ''
  if (!selectedDate.value) return '재생할 날짜를 선택해주세요'
  const sc = Number(shardCount.value)
  if (!sc || Number.isNaN(sc) || sc < 1 || sc > 20) return '샤드 수는 1~20 사이여야 합니다'
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
  precheckMessage.value = '사전 점검 준비 완료 (백엔드 연동 전)'
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
  startMessage.value = '재생 시작 요청 준비됨 (백엔드 연동 전)'
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
          <label>재생 날짜</label>
          <input v-model="selectedDate" type="date" />
        </div>

        <div class="form-field">
          <label>재생 배속 (속도)</label>
          <select v-model.number="speed">
            <option v-for="s in speedOptions" :key="s" :value="s">{{ s }}×</option>
          </select>
        </div>

        <div class="form-field">
          <label>샤드 수 (shardCount)</label>
          <input v-model.number="shardCount" type="number" min="1" max="20" />
          <p class="date-hint">샤드 수는 1~20 사이의 정수입니다. 백엔드 연동 시 shardCount로 전달됩니다.</p>
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

        <div class="bar-chart placeholder">
          <div class="empty-center">
            <strong>시나리오 미리보기: 데이터 연동 예정</strong>
            <div class="empty-sub">백엔드 연동 전에는 샘플 미리보기만 표시됩니다.</div>
          </div>
        </div>

        <div class="status-box">
          <div class="status-left">
            <span class="status-dot"></span>
            <span>상태 확인 전</span>
          </div>
          <div class="status-right">데이터 연동 예정</div>
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
