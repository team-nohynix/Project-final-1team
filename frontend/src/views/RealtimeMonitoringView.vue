<script setup lang="ts">
import { ref, computed, onUnmounted } from 'vue'

// Grafana dashboard URL comes from Vite environment variable
// Do NOT hardcode credentials or secrets in frontend code.
const grafanaEnv = (import.meta as any).env?.VITE_GRAFANA_DASHBOARD_URL || ''
const grafanaUrl = ref<string>(grafanaEnv)

const isAllowedUrl = (u: string) => {
  if (!u) return false
  try {
    const parsed = new URL(u)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:'
  } catch (e) {
    return false
  }
}

const hasValidUrl = computed(() => isAllowedUrl(grafanaUrl.value))

// iframe load/error state
const iframeLoaded = ref(false)
const iframeError = ref(false)
const iframeLoading = ref(false)
let loadTimeout: ReturnType<typeof setTimeout> | null = null

const startIframeLoad = () => {
  iframeLoaded.value = false
  iframeError.value = false
  iframeLoading.value = true
  if (loadTimeout) clearTimeout(loadTimeout)
  // if not loaded within 8s, consider it blocked or requiring login
  loadTimeout = setTimeout(() => {
    if (!iframeLoaded.value) {
      iframeLoading.value = false
      iframeError.value = true
    }
  }, 8000)
}

const onIframeLoad = () => {
  iframeLoaded.value = true
  iframeLoading.value = false
  iframeError.value = false
  if (loadTimeout) {
    clearTimeout(loadTimeout)
    loadTimeout = null
  }
}

onUnmounted(() => {
  if (loadTimeout) clearTimeout(loadTimeout)
})
</script>

<template>
  <div class="monitoring-page">
    <header class="page-header">
      <h2>실시간 성능 모니터링</h2>
      <p class="subtitle">부하·지연·오토스케일링을 동일 시각축으로 관찰</p>
      <hr />
    </header>

    <div class="grafana-area">
      <div class="grafana-card">
        <div class="grafana-header">
          <div>
            <div class="grafana-title">실시간 성능 모니터링 (Grafana)</div>
            <div class="grafana-sub">실제 Grafana 대시보드를 임베드합니다.</div>
          </div>
          <div class="grafana-actions">
            <template v-if="hasValidUrl">
              <div class="grafana-status">
                <span v-if="iframeLoading">연결 중...</span>
                <span v-else-if="iframeLoaded">연결됨</span>
                <span v-else-if="iframeError">차단되었거나 로그인이 필요할 수 있습니다</span>
              </div>
              <a :href="grafanaUrl" target="_blank" rel="noopener noreferrer" class="open-btn">새 탭에서 열기</a>
            </template>
          </div>
        </div>

        <div class="grafana-body">
          <template v-if="!hasValidUrl">
            <div class="grafana-placeholder">
              <h3>Grafana 대시보드 연결 준비 중</h3>
              <p>대시보드 URL을 받으면 실제 모니터링 화면이 표시됩니다.</p>
            </div>
          </template>

          <template v-else>
            <div class="grafana-embed">
              <div v-if="iframeLoading" class="iframe-loading">Grafana 로딩 중...</div>
              <iframe
                :src="grafanaUrl"
                class="grafana-iframe"
                @load="onIframeLoad"
                ref="grafanaIframe"
              ></iframe>
              <!-- always allow open-in-new-tab button for blocked/login cases -->
              <div class="iframe-overlay">
                <a :href="grafanaUrl" target="_blank" rel="noopener noreferrer" class="open-btn large">새 탭에서 열기</a>
              </div>
            </div>
          </template>
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

/* Grafana embed styles */
.grafana-area { margin-top: 12px }
.grafana-card { background:#071826; border:1px solid #163247; border-radius:10px; padding:16px }
.grafana-header { display:flex; justify-content:space-between; align-items:center; margin-bottom:12px }
.grafana-title { font-weight:700 }
.grafana-sub { color:#9fb0c2; font-size:13px }
.grafana-actions { display:flex; gap:12px; align-items:center }
.grafana-status { color:#9fb0c2 }
.open-btn { background:#0e2b32; color:#bfe8db; padding:8px 12px; border-radius:8px; text-decoration:none; border:1px solid #18464d }
.open-btn.large { padding:10px 16px; font-weight:700 }
.grafana-body { min-height:360px }
.grafana-placeholder { color:#9fb0c2; padding:36px; text-align:center }
.grafana-embed { position:relative; height:600px; background:#000; border-radius:8px; overflow:hidden }
.grafana-iframe { width:100%; height:100%; border:0 }
.iframe-loading { position:absolute; inset:0; display:flex; align-items:center; justify-content:center; color:#9fb0c2; background:linear-gradient(180deg, rgba(7,24,38,0.4), rgba(7,24,38,0.4)); z-index:5 }
.iframe-overlay { position:absolute; right:12px; top:12px; z-index:10 }
</style>
