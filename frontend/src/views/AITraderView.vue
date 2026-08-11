<script setup lang="ts">
import { ref, computed, watch } from 'vue'

// Defaults
const defaultScenarioName = 'BTC 급등락 페이퍼 트레이딩'
const defaultTotalOrders = 1800000
const defaultGenerationTime = 60 // seconds

const scenarioName = ref(defaultScenarioName)
const selectedDate = ref('') // YYYY-MM-DD — 백엔드가 이 날짜의 UTC 00:00~다음 날 UTC 00:00 구간을 수집
const totalOrders = ref(defaultTotalOrders)
const generationTime = ref(defaultGenerationTime)

// 날짜 입력의 상한값 (오늘) — 미래 날짜 선택 방지
const formatDateYYYYMMDD = (date: Date) => {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}
const todayDate = formatDateYYYYMMDD(new Date())

// UI messages
const infoMessage = ref('')
const errorMessage = ref('')

// 과거 시세 수집 상태 (백엔드 API 미확정 — 프론트 상태만 준비)
const collectionStatus = ref('idle') // 'idle' | 'collecting' | 'completed' | 'failed'
// 백엔드 응답 예정 필드: 수집 날짜 / 수집 마켓 수 / 수집 성공 마켓 수 / 수집 실패 마켓 수
const collectedData = ref<{
  date?: string
  marketCount?: number
  successCount?: number
  failCount?: number
} | null>(null)

const collectionStatusInfo = computed(() => {
  const statusMap = {
    idle: { label: '수집 요청 전', className: 'status-idle' },
    collecting: { label: '수집 요청 중...', className: 'status-collecting' },
    completed: { label: '수집 완료', className: 'status-completed' },
    failed: { label: '수집 실패', className: 'status-failed' },
  }
  return statusMap[collectionStatus.value]
})

// 수집 날짜가 선택되었을 때만 수집 요청 가능
const canRequestCollection = computed(() => !!selectedDate.value)

const collectionButtonLabel = computed(() => {
  if (collectionStatus.value === 'collecting') return '시세 수집 중...'
  if (collectionStatus.value === 'failed') return '다시 요청'
  if (collectionStatus.value === 'completed') return '수집 완료'
  return '시세 수집 요청'
})

const collectionButtonDisabled = computed(() => {
  if (collectionStatus.value === 'collecting') return true
  if (collectionStatus.value === 'completed') return true
  return !canRequestCollection.value
})

const targetThroughput = computed(() => {
  const secs = Number(generationTime.value) || 1
  const total = Number(totalOrders.value) || 0
  const val = Math.round(total / secs)
  return `${val.toLocaleString()} orders/sec`
})

// 페이퍼 트레이딩 시작에 필요한 필수 입력값이 모두 채워졌는지 여부
// (목표 처리량은 totalOrders/generationTime으로부터 계산되므로 두 값이 유효하면 함께 충족됨)
const canCreate = computed(() => {
  if (!scenarioName.value) return false
  if (!selectedDate.value) return false
  if (!(totalOrders.value > 0)) return false
  if (!(generationTime.value > 0)) return false
  return true
})

// 페이퍼 트레이딩 시작 버튼 활성화 조건: 시세 수집 완료 + 필수 입력값 충족
const canStartPaperTrading = computed(() => {
  return collectionStatus.value === 'completed' && canCreate.value
})

// 시세 수집 요청 시작 (백엔드 API 미확정 — 실제 요청은 아직 구현하지 않음)
const requestMarketData = async () => {
  if (!canRequestCollection.value || collectionStatus.value === 'collecting') return

  collectionStatus.value = 'collecting'
  collectedData.value = null

  try {
    // TODO: 백엔드 시세 수집 요청 API 연결 (예: POST /api/market-data/collect)
    // const payload = { date: selectedDate.value } // 20개 마켓 전체 대상, 해당 날짜 UTC 00:00~다음 날 UTC 00:00 수집
    // const response = await marketDataApi.requestCollection(payload)
    // handleCollectionSuccess(response.data)
  } catch (error) {
    handleCollectionError(error)
  }
}

// 수집 완료 처리 (API 응답 데이터를 그대로 저장)
const handleCollectionSuccess = (data) => {
  collectedData.value = data
  collectionStatus.value = 'completed'
}

// 수집 실패 처리
const handleCollectionError = (error) => {
  console.error('시세 수집 실패:', error)
  collectionStatus.value = 'failed'
}

// 시세 수집 상태를 idle로 초기화
const resetCollection = () => {
  collectionStatus.value = 'idle'
  collectedData.value = null
}

// 수집 날짜가 변경되면 기존 시세 수집 상태(수집 데이터/상태 배지/페이퍼 트레이딩 버튼)를 초기화
watch(selectedDate, () => {
  resetCollection()
})

// 페이퍼 트레이딩 실행 상태 (백엔드 API/응답 규격 미확정 — 프론트 상태만 준비)
const executionStatus = ref('idle') // 'idle' | 'running' | 'success' | 'error'
// 백엔드 응답 타입 확정 전까지는 unknown으로 보관 (TODO: 응답 규격 확정 후 구체 타입 지정)
const paperTradingResult = ref<unknown>(null)
const executionError = ref('')

// 페이퍼 트레이딩 실행 상태를 idle로 초기화
const resetExecution = () => {
  executionStatus.value = 'idle'
  paperTradingResult.value = null
  executionError.value = ''
}

// 페이퍼 트레이딩 시작 (백엔드 API 미확정 — 실제 요청은 아직 구현하지 않음)
const startPaperTrading = async () => {
  if (!canStartPaperTrading.value) return

  executionStatus.value = 'running'
  executionError.value = ''
  paperTradingResult.value = null

  try {
    // TODO: 백엔드 페이퍼 트레이딩 시작 API 연결
    // const response = await paperTradingApi.start({ ... })
    // paperTradingResult.value = response.data
    // executionStatus.value = 'success'
    infoMessage.value = '백엔드 연결 전: 페이퍼 트레이딩 시작 요청이 준비되었습니다.'
  } catch (error) {
    executionError.value = error instanceof Error ? error.message : ''
    executionStatus.value = 'error'
  }
}

const reset = () => {
  scenarioName.value = defaultScenarioName
  selectedDate.value = ''
  totalOrders.value = defaultTotalOrders
  generationTime.value = defaultGenerationTime
  infoMessage.value = ''
  errorMessage.value = ''
  resetCollection()
  resetExecution()
}
</script>

<template>
  <div>
    <header class="page-header">
      <h2>페이퍼 트레이딩</h2>
      <p class="subtitle">과거 시세를 기반으로 매수·매도 주문 패턴을 생성하고 기록합니다.</p>
      <hr />
    </header>

    <div class="content-grid">
      <section class="panel left-panel">
        <h3 class="panel-title">페이퍼 트레이딩 설정</h3>
        <p class="panel-sub">과거 시세와 생성 조건을 설정합니다</p>

        <div class="form-field">
          <label>페이퍼 트레이딩 이름</label>
          <input v-model="scenarioName" type="text" />
        </div>

        <div class="form-field">
          <label>대상 마켓</label>
          <div class="readonly-input">업비트 KRW 마켓 20개 전체</div>
        </div>

        <div class="form-field">
          <label>시세 수집 날짜</label>
          <input
            v-model="selectedDate"
            type="date"
            :max="todayDate"
            :disabled="collectionStatus === 'collecting'"
          />
          <p class="date-hint">
            선택한 날짜의 UTC 00:00부터 다음 날 UTC 00:00까지 20개 마켓의 시세를 수집합니다.
          </p>
        </div>

        <div class="collection-section">
          <div class="collection-header">
            <span class="collection-title">과거 시세 수집</span>
            <span class="collection-status-badge" :class="collectionStatusInfo.className">
              <span class="dot"></span>{{ collectionStatusInfo.label }}
            </span>
          </div>

          <div class="collection-data">
            <template v-if="collectionStatus === 'idle'">수집된 시세 데이터 없음</template>
            <template v-else-if="collectionStatus === 'collecting'">시세 데이터를 수집하고 있습니다.</template>
            <template v-else-if="collectionStatus === 'failed'">시세 수집에 실패했습니다. 다시 요청해주세요.</template>
            <template v-else-if="collectionStatus === 'completed'">
              <div class="collection-data-list">
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 날짜</span>
                  <span class="collection-data-value">{{ collectedData?.date ?? '-' }}</span>
                </div>
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 마켓 수</span>
                  <span class="collection-data-value">{{ collectedData?.marketCount ?? '-' }}</span>
                </div>
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 성공 마켓 수</span>
                  <span class="collection-data-value">{{ collectedData?.successCount ?? '-' }}</span>
                </div>
                <div class="collection-data-row">
                  <span class="collection-data-key">수집 실패 마켓 수</span>
                  <span class="collection-data-value">{{ collectedData?.failCount ?? '-' }}</span>
                </div>
              </div>
            </template>
          </div>

          <button
            type="button"
            class="btn-primary collection-request-btn"
            :disabled="collectionButtonDisabled"
            @click="requestMarketData"
          >
            {{ collectionButtonLabel }}
          </button>
        </div>

        <div class="form-field two-cols">
          <div>
            <label>목표 주문 수</label>
            <input v-model.number="totalOrders" type="number" />
          </div>
          <div>
            <label>생성 시간 (sec)</label>
            <input v-model.number="generationTime" type="number" />
          </div>
        </div>

        <div class="form-field">
          <label>목표 처리량</label>
          <div class="readonly-input">{{ targetThroughput }}</div>
        </div>

        <div class="actions">
          <button class="btn-primary" :disabled="!canStartPaperTrading" @click="startPaperTrading">
            페이퍼 트레이딩 시작
          </button>
          <button class="btn-dark" @click="reset">초기화</button>
        </div>

        <div v-if="errorMessage" class="error-note">{{ errorMessage }}</div>
        <div v-if="infoMessage" class="info-note">{{ infoMessage }}</div>
      </section>

      <aside class="panel right-panel">
        <h3 class="panel-title">페이퍼 트레이딩 결과</h3>
        <p class="panel-sub">페이퍼 트레이딩 실행 상태와 백엔드에서 받은 결과를 표시합니다.</p>

        <div class="result-panel">
          <template v-if="executionStatus === 'idle'">
            <div class="result-title">페이퍼 트레이딩 실행 전</div>
            <div class="result-desc">페이퍼 트레이딩을 시작하면 실행 결과가 표시됩니다.</div>
          </template>

          <template v-else-if="executionStatus === 'running'">
            <div class="result-spinner"></div>
            <div class="result-title">페이퍼 트레이딩 진행 중</div>
            <div class="result-desc">AI 트레이더가 주문을 생성하고 기록하고 있습니다.</div>
          </template>

          <template v-else-if="executionStatus === 'error'">
            <div class="result-title result-title-error">페이퍼 트레이딩 실행 실패</div>
            <div class="result-desc">
              {{ executionError || '페이퍼 트레이딩 실행 중 오류가 발생했습니다.' }}
            </div>
          </template>

          <template v-else-if="executionStatus === 'success'">
            <div class="result-desc">백엔드에서 실행 결과를 받았습니다.</div>
            <!-- TODO: 백엔드 응답 명세(paperTradingResult) 확정 후 상세 결과 UI 구현 -->
          </template>
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
.two-cols {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.date-hint {
  margin: 8px 0 0 0;
  color: #9fb0c2;
  font-size: 12px;
}
.readonly-input {
  padding: 12px 14px;
  background: #072037;
  border: 1px solid #163247;
  color: #cfe6ff;
  border-radius: 8px;
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
.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
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
.result-panel {
  min-height: 260px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  text-align: center;
  gap: 8px;
  background: #071a28;
  border: 1px solid #122a3d;
  border-radius: 8px;
  padding: 32px 16px;
}
.result-title {
  font-weight: 700;
  font-size: 15px;
  color: #d7e8fb;
}
.result-title-error {
  color: #ff6b6b;
}
.result-desc {
  color: #9fb0c2;
  font-size: 13px;
}
.result-spinner {
  width: 28px;
  height: 28px;
  margin-bottom: 4px;
  border: 3px solid #163247;
  border-top-color: #3f86ff;
  border-radius: 50%;
  animation: result-spin 0.8s linear infinite;
}
@keyframes result-spin {
  to {
    transform: rotate(360deg);
  }
}

.error-note {
  margin-top: 10px;
  color: #ff6b6b;
  font-weight: 700;
}

.info-note {
  margin-top: 8px;
  color: #cfe6ff;
  background: #072037;
  padding: 10px;
  border-radius: 8px;
  border: 1px solid #163247;
}

.collection-section {
  margin: 16px 0;
  background: #081826;
  border: 1px solid #122a3d;
  border-radius: 10px;
  padding: 14px;
}
.collection-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.collection-title {
  font-weight: 700;
  color: #d7e8fb;
  font-size: 14px;
}
.collection-status-badge {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  border-radius: 20px;
  font-weight: 700;
  font-size: 12px;
}
.collection-status-badge .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.collection-status-badge.status-idle {
  background: rgba(159, 176, 194, 0.12);
  color: #9fb0c2;
}
.collection-status-badge.status-idle .dot {
  background: #9fb0c2;
}
.collection-status-badge.status-collecting {
  background: rgba(63, 134, 255, 0.12);
  color: #7fb2ff;
}
.collection-status-badge.status-collecting .dot {
  background: #3f86ff;
}
.collection-status-badge.status-completed {
  background: rgba(46, 211, 154, 0.12);
  color: #2ed39a;
}
.collection-status-badge.status-completed .dot {
  background: #2ed39a;
}
.collection-status-badge.status-failed {
  background: rgba(255, 107, 107, 0.12);
  color: #ff6b6b;
}
.collection-status-badge.status-failed .dot {
  background: #ff6b6b;
}
.collection-data {
  color: #9fb0c2;
  font-size: 13px;
}
.collection-data-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.collection-data-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  color: #cfe6ff;
}
.collection-data-key {
  color: #9fb0c2;
}
.collection-request-btn {
  width: 100%;
  margin-top: 12px;
}

@media (max-width: 900px) {
  .content-grid {
    grid-template-columns: 1fr;
  }
}
</style>
