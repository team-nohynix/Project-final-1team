<script setup>
import { ref, computed, onMounted } from 'vue'

const engines = ref([])
const loadingInitial = ref(false)
const loading = ref(false) // for manual refresh
const error = ref(null) // { status, message }

const apiPath = '/recorder-api/v1/matching/engines'

const fetchEngines = async ({ isRefresh = false } = {}) => {
  try {
    if (!isRefresh) {
      loadingInitial.value = true
      error.value = null
    } else {
      loading.value = true
      // do not clear existing engines while refreshing
      error.value = null
    }

    const res = await fetch(apiPath)
    if (!res.ok) {
      const text = await res.text().catch(() => res.statusText)
      throw { status: res.status, message: text || res.statusText }
    }

    const body = await res.json()

    // Only accept explicit engines array from API
    if (!Array.isArray(body.engines)) {
      engines.value = []
    } else {
      engines.value = body.engines
    }
  } catch (e) {
    // Normalize error
    if (e && typeof e === 'object' && 'status' in e) {
      error.value = { status: e.status, message: String(e.message || '') }
    } else {
      error.value = { status: null, message: String(e || 'Unknown error') }
    }
  } finally {
    loadingInitial.value = false
    loading.value = false
  }
}

onMounted(() => fetchEngines())

const activeEngineCount = computed(() => engines.value.length)
const totalAssignedMarkets = computed(() => engines.value.reduce((acc, e) => acc + (Array.isArray(e.markets) ? e.markets.length : 0), 0))

const formatAssignedAt = (ts) => {
  if (!ts) return '--'
  const parsed = Date.parse(ts)
  if (isNaN(parsed)) return '--'
  try {
    return new Date(parsed).toLocaleString('ko-KR', { timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
  } catch {
    return '--'
  }
}
</script>

<template>
  <div class="matching-page">
    <header class="page-header">
      <div>
        <h2>매칭 엔진</h2>
        <p>실제 매칭 엔진의 인스턴스별 배정 현황을 표시합니다.</p>
      </div>
      <div class="header-actions">
        <button @click="fetchEngines({ isRefresh: true })" :disabled="loading || loadingInitial">
          {{ loading ? '새로고침 중...' : '새로고침' }}
        </button>
      </div>
    </header>

    <main>
      <section v-if="loadingInitial" class="panel centered">조회 중...</section>

      <section v-else-if="error" class="panel error-panel">
        <h3>오류 발생</h3>
        <p>상태 코드: <strong>{{ error.status ?? '-' }}</strong></p>
        <p>메시지: <strong>{{ error.message }}</strong></p>
        <button @click="fetchEngines()">다시 조회</button>
      </section>

      <section v-else class="panel">
        <div class="summary-row">
          <div>현재 활성 매칭 엔진 수: <strong>{{ activeEngineCount }}</strong></div>
          <div>전체 배정 마켓 수: <strong>{{ totalAssignedMarkets }}</strong></div>
        </div>

        <div v-if="engines.length === 0" class="no-data">현재 배정된 매칭 엔진이 없습니다.</div>

        <div v-else class="engine-list">
          <div v-for="engine in engines" :key="engine.engineInstanceId" class="engine-card">
            <div class="engine-header">
              <div class="engine-id">{{ engine.engineInstanceId }}</div>
              <div class="engine-count">배정 마켓 수: <strong>{{ Array.isArray(engine.markets) ? engine.markets.length : 0 }}</strong></div>
            </div>

            <div class="engine-markets">
              <template v-if="!Array.isArray(engine.markets) || engine.markets.length === 0">
                <div class="no-markets">배정된 마켓 없음</div>
              </template>
              <template v-else>
                <table class="markets-table">
                  <thead>
                    <tr>
                      <th>마켓</th>
                      <th>Assigned At (KST)</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="m in engine.markets" :key="m.market">
                      <td>{{ m.market }}</td>
                      <td>{{ formatAssignedAt(m.assignedAt) }}</td>
                    </tr>
                  </tbody>
                </table>
              </template>
            </div>
          </div>
        </div>
      </section>
    </main>
  </div>
</template>

<style scoped>
.matching-page { color: #f3f7fc; }
.page-header { display:flex; justify-content:space-between; align-items:center; gap:20px; }
.page-header h2 { margin:0 }
.header-actions button { background:#0b4a5b; color:#fff; border:none; padding:8px 12px; border-radius:6px }
.panel { background:#071824; border:1px solid #173141; padding:16px; border-radius:8px; margin-top:16px }
.centered { text-align:center; padding:40px }
.error-panel { color:#ffb3b3 }
.summary-row { display:flex; gap:20px; justify-content:space-between }
.engine-list { margin-top:12px; display:flex; flex-direction:column; gap:12px }
.engine-card { border:1px solid #173141; padding:12px; border-radius:8px; background:#08151a }
.engine-header { display:flex; justify-content:space-between; align-items:center }
.engine-id { font-weight:700 }
.markets-table { width:400px; border-collapse:collapse; margin:8px 0 0; table-layout: fixed }
.markets-table th, .markets-table td { text-align:left; padding:6px 8px; border-bottom:1px solid #0f2a33 }
.markets-table th:first-child, .markets-table td:first-child { width: 140px }
.no-data, .no-markets { color:#9fb0c1 }

@media (max-width: 900px) {
  .summary-row { flex-direction:column; gap:8px }
}
</style>

