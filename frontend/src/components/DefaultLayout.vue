<script setup>
import { ref, watch, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'

const router = useRouter()
const route = useRoute()

// "전체 초기화" 버튼(2026-08-26 요청) — 부하테스트 사이사이 잔여 상태를 한 번에
// 지우기 위한 관리자용 단축 버튼. 이미 있는 관리자 API 둘을 순서대로 호출할
// 뿐이다: (1) 미종결 주문 일괄 취소(POST .../admin/cleanup-unresolved-orders),
// (2) 매칭엔진 호가창 강제 리셋(POST .../admin/reset-matching-engine-book?force=true,
// orderapi/adminreset.go — 롤아웃 재시작+Redis 스냅샷 삭제+워터마크 강제 재설정).
// 순서가 중요하다 — 먼저 주문을 취소해야 그 CANCEL이 매칭엔진 재시작 전/중에
// 소비될 여지가 있고, force reset이 그 뒤를 확실히 비워준다.
//
// **활성 노드/레플리카 수는 여기서 강제로 안 내린다** — 이 세션 초반에
// kubectl scale로 직접 낮춰봤다가 KEDA HPA가 몇 초 안에 그대로 되돌리는 걸
// 실측했다(HPA가 활성 상태인 한 매니페스트 밖 수동 조정은 다음 reconcile
// 주기에 덮어써짐 — 파드 churn만 유발). 대신 위 두 액션으로 스케일업의 실제
// 원인(컨슈머 랙/호가창 크기)을 없애면, HPA가 스스로 최소치로 내려간다 —
// 이제 스케일다운 stabilization window가 60초로 줄어있어(매칭엔진/기록기
// ScaledObject 참고) 몇 분 안에 자연스럽게 정리된다.
const resetInFlight = ref(false)
const resetMessage = ref('')
let resetMessageTimer = null

async function resetSystemState() {
  if (resetInFlight.value) return
  const ok = window.confirm(
    '처리 대기 중인 모든 주문을 취소하고 매칭엔진 호가창을 완전히 비웁니다.\n' +
      '지금 진행 중인 테스트가 있다면 그 결과도 영향을 받습니다. 계속할까요?',
  )
  if (!ok) return

  clearTimeout(resetMessageTimer)
  resetInFlight.value = true
  resetMessage.value = '미종결 주문 정리 요청 중...'
  try {
    const cleanupRes = await fetch('/order-api/v1/admin/cleanup-unresolved-orders', { method: 'POST' })
    // 409(CLEANUP_ALREADY_IN_PROGRESS)는 실패가 아니라 "이미 진행 중"이라는
    // 뜻이라 그대로 다음 단계(호가창 리셋)로 진행해도 안전하다.
    if (!cleanupRes.ok && cleanupRes.status !== 409) {
      throw new Error(`미종결 주문 정리 요청 실패 (${cleanupRes.status})`)
    }

    resetMessage.value = '매칭엔진 호가창 리셋 요청 중...'
    // 2026-08-26: 이 요청 자체가 응답을 기다리던 옛 버전은, 매칭엔진이
    // 여러 레플리카로 떠 있을 때(부하테스트 중, 이 버튼이 가장 필요한
    // 상황) 롤아웃 완료 대기가 CloudFront 오리진 응답 한계를 넘겨 브라우저가
    // "실패"로 표시하는 문제가 있었다(서버 쪽은 실제로 항상 끝까지 완료되고
    // 있었음, orderapi/adminreset.go 참고) — 이제 서버가 즉시 202를 반환하고
    // 백그라운드에서 진행하므로, 여기서 GET .../status를 폴링해 실제 완료를
    // 기다린다.
    const startRes = await fetch('/order-api/v1/admin/reset-matching-engine-book?force=true', { method: 'POST' })
    if (startRes.status !== 202 && startRes.status !== 409) {
      const body = await startRes.text().catch(() => '')
      throw new Error(`매칭엔진 호가창 리셋 요청 실패 (${startRes.status}) ${body}`)
    }

    for (let attempt = 0; attempt < 60; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 2000))
      const statusRes = await fetch('/order-api/v1/admin/reset-matching-engine-book/status')
      if (!statusRes.ok) continue
      const status = await statusRes.json()
      if (status.status === 'IN_PROGRESS') {
        resetMessage.value = `매칭엔진 호가창 리셋 중... (${status.step || '진행 중'})`
        continue
      }
      if (status.status === 'FAILED') {
        throw new Error(status.message || '매칭엔진 호가창 리셋 실패')
      }
      // COMPLETED — resetInFlight/타이머 정리는 finally에서 공통으로 처리한다.
      resetMessage.value = '초기화 완료 — 미종결 주문 정리와 호가창 리셋을 마쳤습니다.'
      return
    }
    throw new Error('매칭엔진 호가창 리셋 상태 확인이 시간 초과됐습니다 — 서버에서는 계속 진행 중일 수 있습니다.')
  } catch (e) {
    resetMessage.value = `초기화 실패: ${e.message || e}`
  } finally {
    resetInFlight.value = false
    resetMessageTimer = setTimeout(() => {
      resetMessage.value = ''
    }, 10000)
  }
}

const openMenu = ref('')

const updateOpenMenu = () => {
  const p = route.path || ''
  if (p.startsWith('/load-test')) openMenu.value = 'loadtest'
  else if (
    p.startsWith('/matching-engine') ||
    p.startsWith('/market-orderbook')
  )
    openMenu.value = 'trading'
  else openMenu.value = ''
}

onMounted(() => updateOpenMenu())
watch(
  () => route.path,
  () => updateOpenMenu(),
)

const toggleMenu = (menu) => {
  openMenu.value = openMenu.value === menu ? '' : menu
}

const go = (path) => {
  router.push(path)
}

const isActive = (path) => route.path === path
</script>

<template>
  <div class="app-layout">
    <aside class="sidebar">
      <div class="logo-area">
        <h1>TRUSS</h1>
        <p>K8s Trading Lab</p>
      </div>

      <nav class="navigation">
        <button
          class="submenu-item"
          :class="{ selected: isActive('/') }"
          type="button"
          @click.prevent="go('/')"
        >
          <span class="menu-dot"></span>
          시스템 종합 현황
        </button>

        <button class="menu-button" type="button" @click="toggleMenu('trading')">
          <span>거래 처리</span>
          <span class="arrow" :class="{ open: openMenu === 'trading' }">›</span>
        </button>

        <div v-if="openMenu === 'trading'" class="submenu">
          <button
            class="submenu-item"
            :class="{ selected: isActive('/matching-engine') }"
            type="button"
            @click.prevent="go('/matching-engine')"
          >
            <span class="menu-dot"></span>
            매칭 엔진
          </button>

          <button
            class="submenu-item"
            :class="{ selected: isActive('/market-orderbook') }"
            type="button"
            @click.prevent="go('/market-orderbook')"
          >
            <span class="menu-dot"></span>
            마켓·호가창
          </button>
        </div>

        <button class="menu-button" type="button" @click="toggleMenu('loadtest')">
          <span>부하 테스트</span>
          <span class="arrow" :class="{ open: openMenu === 'loadtest' }">›</span>
        </button>

        <div v-if="openMenu === 'loadtest'" class="submenu">
          <button
            class="submenu-item"
            :class="{ selected: isActive('/load-test/replay') }"
            type="button"
            @click.prevent="go('/load-test/replay')"
          >
            <span class="menu-dot"></span>
            주문 재생
          </button>

          <button
            class="submenu-item"
            :class="{ selected: isActive('/load-test/ai-trader') }"
            type="button"
            @click.prevent="go('/load-test/ai-trader')"
          >
            <span class="menu-dot"></span>
            페이퍼 트레이딩
          </button>
        </div>

        <button
          class="submenu-item"
          :class="{ selected: isActive('/test-results') }"
          type="button"
          @click.prevent="go('/test-results')"
        >
          <span class="menu-dot"></span>
          결과 분석
        </button>

        <button
          class="submenu-item"
          :class="{ selected: isActive('/market-stream') }"
          type="button"
          @click.prevent="go('/market-stream')"
        >
          <span class="menu-dot"></span>
          시세 조회
        </button>
      </nav>

      <div class="system-badge">
        <span class="status-dot"></span>
        시스템 정상
      </div>

      <button
        class="reset-button"
        type="button"
        :disabled="resetInFlight"
        @click="resetSystemState"
      >
        {{ resetInFlight ? '초기화 중...' : '전체 초기화' }}
      </button>
      <p v-if="resetMessage" class="reset-message">{{ resetMessage }}</p>
    </aside>

    <main class="main-content">
      <router-view v-slot="{ Component, route }">
        <keep-alive>
          <component :is="Component" v-if="route.meta && route.meta.keepAlive" :key="route.name" />
        </keep-alive>

        <component :is="Component" v-if="!(route.meta && route.meta.keepAlive)" :key="route.name" />
      </router-view>
    </main>
  </div>
</template>

<style>
/* Sidebar and layout styles (moved from DashboardView to be shared) */
* {
  box-sizing: border-box;
}

body {
  margin: 0;
  min-width: 1200px;
  min-height: 100vh;
  background: #07111f;
  color: #f3f7fc;
  font-family:
    Inter,
    'Noto Sans KR',
    -apple-system,
    BlinkMacSystemFont,
    'Segoe UI',
    sans-serif;
}

button {
  font: inherit;
}

.app-layout {
  display: flex;
  min-height: 100vh;
  background: #07111f;
}

.sidebar {
  position: fixed;
  inset: 0 auto 0 0;
  display: flex;
  width: 230px;
  padding: 30px 20px 24px;
  flex-direction: column;
  background: #091625;
  border-right: 1px solid #172a3e;
}

.logo-area {
  padding: 0 8px 28px;
}

.logo-area h1 {
  margin: 0;
  font-size: 25px;
  letter-spacing: 1px;
}

.logo-area p {
  margin: 5px 0 0;
  color: #20c8e8;
  font-size: 12px;
}

.navigation {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.menu-button {
  display: flex;
  width: 100%;
  padding: 12px;
  align-items: center;
  justify-content: space-between;
  color: #8ea2b8;
  background: transparent;
  border: 0;
  border-radius: 10px;
  cursor: pointer;
}

.menu-button:hover,
.menu-button.active {
  color: #f3f7fc;
  background: #0d1b2a;
}

.arrow {
  font-size: 20px;
  transition: transform 0.2s ease;
}

.arrow.open {
  color: #3478f6;
  transform: rotate(90deg);
}

.submenu {
  padding: 0 0 5px 8px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.submenu-item {
  display: flex;
  width: 100%;
  padding: 11px 12px;
  align-items: center;
  gap: 10px;
  color: #f3f7fc;
  background: #11243a;
  border: 1px solid transparent;
  border-radius: 9px;
  cursor: pointer;
  white-space: nowrap;
}

/* Selected submenu: darker blue background with blue border and bright text/dot */
.submenu-item.selected {
  background: #05345f; /* 진한 파란색 배경 */
  border: 1px solid #3478f6; /* 파란색 테두리 */
  color: #eaf4ff; /* 밝은 글자색 */
}

.submenu-item.selected .menu-dot {
  background: #eaf4ff; /* 밝은 점 아이콘 */
}

.menu-dot {
  width: 8px;
  height: 8px;
  background: #556172; /* 기본: 회색 점 */
  border-radius: 50%;
}

.system-badge {
  display: flex;
  margin-top: auto;
  padding: 10px 13px;
  align-items: center;
  gap: 8px;
  color: #2ed39a;
  background: #11243a;
  border-radius: 20px;
  font-size: 12px;
  font-weight: 700;
}

.status-dot,
.live-dot {
  width: 8px;
  height: 8px;
  background: #2ed39a;
  border-radius: 50%;
}

.reset-button {
  margin-top: 8px;
  padding: 8px 13px;
  color: #ff9f7a;
  background: #2a1710;
  border: 1px solid #4a2a1a;
  border-radius: 20px;
  font-size: 11.5px;
  font-weight: 700;
  cursor: pointer;
}

.reset-button:hover:not(:disabled) {
  background: #3a2015;
  border-color: #c97a2e;
}

.reset-button:disabled {
  color: #8ea2b8;
  cursor: not-allowed;
  opacity: 0.7;
}

.reset-message {
  margin: 6px 2px 0;
  color: #9fb0c2;
  font-size: 11px;
  line-height: 1.4;
}

.main-content {
  /* Use remaining space next to the fixed sidebar */
  flex: 1;
  width: auto;
  max-width: none;
  min-width: 0;
  margin-left: 230px;
  padding: 32px;
}

/* Simple helpers for panels to match existing dashboard */
.panel {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 15px;
  padding: 20px;
}

.metric-card {
  padding: 18px;
}
</style>
