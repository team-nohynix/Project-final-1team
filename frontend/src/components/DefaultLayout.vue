<script setup>
import { ref, watch, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'

const router = useRouter()
const route = useRoute()

const openMenu = ref('')

const updateOpenMenu = () => {
  const p = route.path || ''
  if (p === '/' || p === '') openMenu.value = 'overview'
  else if (p.startsWith('/load-test')) openMenu.value = 'loadtest'
  else if (
    p.startsWith('/orders') ||
    p.startsWith('/matching-engine') ||
    p.startsWith('/market-orderbook')
  )
    openMenu.value = 'trading'
  else if (
    p.startsWith('/monitoring') ||
    p.startsWith('/test-results') ||
    p.startsWith('/fault-recovery')
  )
    openMenu.value = 'observe'
  else if (p.startsWith('/market-stream') || p.startsWith('/deployment-ops')) openMenu.value = 'data'
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
        <button class="menu-button" type="button" @click="toggleMenu('overview')">
          <span>종합 현황</span>
          <span class="arrow" :class="{ open: openMenu === 'overview' }">›</span>
        </button>

        <div v-if="openMenu === 'overview'" class="submenu">
          <button class="submenu-item selected" type="button" @click="go('/')">
            <span class="menu-dot"></span>
            시스템 종합 현황
          </button>
        </div>

        <button class="menu-button" type="button" @click="toggleMenu('trading')">
          <span>거래 처리</span>
          <span class="arrow" :class="{ open: openMenu === 'trading' }">›</span>
        </button>

        <div v-if="openMenu === 'trading'" class="submenu">
          <button
            class="submenu-item"
            :class="{ selected: isActive('/orders') }"
            type="button"
            @click.prevent="go('/orders')"
          >
            <span class="menu-dot"></span>
            주문 API 검증
          </button>

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

        <button class="menu-button" type="button" @click="toggleMenu('observe')">
          <span>관찰·검증</span>
          <span class="arrow" :class="{ open: openMenu === 'observe' }">›</span>
        </button>

        <div v-if="openMenu === 'observe'" class="submenu">
          <button
            class="submenu-item"
            :class="{ selected: isActive('/monitoring') }"
            type="button"
            @click.prevent="go('/monitoring')"
          >
            <span class="menu-dot"></span>
            실시간 모니터링
          </button>

          <button
            class="submenu-item"
            :class="{ selected: isActive('/test-results') }"
            type="button"
            @click.prevent="go('/test-results')"
          >
            <span class="menu-dot"></span>
            결과 추적
          </button>

          <button
            class="submenu-item"
            :class="{ selected: isActive('/fault-recovery') }"
            type="button"
            @click.prevent="go('/fault-recovery')"
          >
            <span class="menu-dot"></span>
            장애 주입·복구
          </button>
        </div>

        <button class="menu-button" type="button" @click="toggleMenu('data')">
          <span>데이터·운영</span>
          <span class="arrow" :class="{ open: openMenu === 'data' }">›</span>
        </button>

        <div v-if="openMenu === 'data'" class="submenu">
          <button
            class="submenu-item"
            :class="{ selected: isActive('/market-stream') }"
            type="button"
            @click.prevent="go('/market-stream')"
          >
            <span class="menu-dot"></span>
            시세 처리
          </button>

          <button
            class="submenu-item"
            :class="{ selected: isActive('/deployment-ops') }"
            type="button"
            @click.prevent="go('/deployment-ops')"
          >
            <span class="menu-dot"></span>
            배포·운영
          </button>
        </div>
      </nav>

      <div class="system-badge">
        <span class="status-dot"></span>
        시스템 정상
      </div>
    </aside>

    <main class="main-content">
      <router-view />
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
