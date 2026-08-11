<script setup lang="ts">
import { ref } from 'vue'

const release = ref({
  version: 'v0.8.4',
  commit: '8f21a0d',
  flow: 'dev → staging → production',
  status: '배포 중',
})

const stages = ref([
  { name: '규격 검사', status: '통과', state: 'done' },
  { name: '자동 테스트', status: '통과', state: 'done' },
  { name: '이미지 빌드', status: '통과', state: 'done' },
  { name: '배포', status: '실행 중', state: 'active' },
  { name: '배포 검증', status: '대기', state: 'pending' },
])

const schemaChecks = ref([
  { name: 'OpenAPI response schema', result: '통과' },
  { name: 'Kafka order schema', result: '통과' },
  { name: 'Kafka execution schema', result: '통과' },
  { name: 'Replay file schema', result: '통과' },
  { name: 'Breaking change detection', result: '0 changes' },
])

const environments = ref([
  { name: 'DEV', cluster: 'truss-dev', pods: '3 pods', color: '#20c8e8' },
  { name: 'STAGING', cluster: 'truss-stg', pods: '6 pods', color: '#ff9f43' },
  { name: 'PRODUCTION', cluster: 'truss-prod', pods: '12 pods', color: '#2ed39a' },
])

const rollouts = ref([
  { name: 'matching-engine v0.8.3', progress: '6 pods', status: '종료 준비', state: 'warn' },
  { name: 'matching-engine v0.8.4', progress: '6 / 12 pods', status: '시작 중', state: 'active' },
  { name: 'order-api v0.8.4', progress: '4 / 4 pods', status: '정상', state: 'ok' },
])
</script>

<template>
  <div class="deploy-page">
    <header class="deploy-header">
      <h2 class="deploy-title">배포·운영</h2>
      <p class="deploy-subtitle">규격 검증·자동 테스트·CI/CD·무중단 배포·환경 분리</p>
      <hr class="deploy-divider" />
    </header>

    <div class="deploy-release-card">
      <div class="deploy-release-left">
        <div class="deploy-release-name">Release {{ release.version }}</div>
        <div class="deploy-release-meta">commit {{ release.commit }} · {{ release.flow }}</div>
      </div>
      <div class="deploy-release-right">
        <span class="deploy-dot blue"></span>
        <span class="deploy-badge active">{{ release.status }}</span>
      </div>
    </div>

    <div class="deploy-stage-card">
      <div class="deploy-stage-timeline">
        <div v-for="(s, i) in stages" :key="i" class="deploy-stage-step" :class="s.state">
          <div class="deploy-stage-circle">{{ i + 1 }}</div>
          <div class="deploy-stage-name">{{ s.name }}</div>
          <div class="deploy-stage-status">{{ s.status }}</div>
        </div>
      </div>
    </div>

    <div class="deploy-middle-grid">
      <div class="deploy-schema-card">
        <h4 class="deploy-card-title">규격 검증</h4>
        <div v-for="(c, i) in schemaChecks" :key="i" class="deploy-schema-row">
          <div class="deploy-schema-name">{{ c.name }}</div>
          <span class="deploy-badge pass">{{ c.result }}</span>
        </div>
      </div>

      <div class="deploy-env-card">
        <h4 class="deploy-card-title">환경별 상태</h4>
        <div v-for="(e, i) in environments" :key="i" class="deploy-env-row">
          <span class="deploy-env-badge" :style="{ color: e.color, borderColor: e.color }">{{ e.name }}</span>
          <div class="deploy-env-cluster">{{ e.cluster }}</div>
          <div class="deploy-env-pods">{{ e.pods }}</div>
        </div>
      </div>
    </div>

    <div class="deploy-rollout-card">
      <h4 class="deploy-card-title">무중단 배포</h4>
      <p class="deploy-rollout-desc">부하가 걸린 상태에서도 오류율 0.1% 이하를 유지합니다.</p>
      <div v-for="(r, i) in rollouts" :key="i" class="deploy-rollout-row">
        <div class="deploy-rollout-name">{{ r.name }}</div>
        <div class="deploy-rollout-progress">{{ r.progress }}</div>
        <span class="deploy-badge" :class="r.state">{{ r.status }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.deploy-page {
  width: 100%;
  max-width: none;
  min-width: 0;
  box-sizing: border-box;
  padding-bottom: 32px;
}
.deploy-page > * + * {
  margin-top: 16px;
}

.deploy-title { margin: 0 0 6px }
.deploy-subtitle { color: #9fb0c2; font-size: 13px; margin: 0 }
.deploy-divider {
  border: 0;
  height: 1px;
  background: #1b2e46;
  margin: 12px 0 0;
}

.deploy-card-title { margin: 0 0 14px; font-weight: 700; font-size: 15px }

.deploy-release-card {
  width: 100%;
  min-height: 100px;
  box-sizing: border-box;
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
}
.deploy-release-name { font-weight: 700; font-size: 15px }
.deploy-release-meta { color: #9fb0c2; margin-top: 6px; font-size: 13px }
.deploy-release-right { display: flex; align-items: center; gap: 8px }
.deploy-dot { width: 8px; height: 8px; border-radius: 50% }
.deploy-dot.blue { background: #3478f6 }

.deploy-badge {
  display: inline-flex;
  align-items: center;
  padding: 6px 12px;
  border-radius: 20px;
  font-weight: 700;
  font-size: 12px;
}
.deploy-badge.active { background: #0d2b57; color: #7fb2ff }
.deploy-badge.pass { background: #072a1a; color: #2ed39a }
.deploy-badge.ok { background: #072a1a; color: #2ed39a }
.deploy-badge.warn { background: #2b1508; color: #ff9f43 }

.deploy-stage-card {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
  box-sizing: border-box;
}
.deploy-stage-timeline {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
}
.deploy-stage-step {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  text-align: center;
  padding: 0 6px;
}
.deploy-stage-step:not(:last-child)::after {
  content: '';
  position: absolute;
  top: 18px;
  left: 50%;
  width: 100%;
  height: 2px;
  background: #172a3e;
  z-index: 0;
}
.deploy-stage-circle {
  position: relative;
  z-index: 1;
  width: 36px;
  height: 36px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  font-size: 13px;
  color: #fff;
  margin-bottom: 8px;
  background: #556172;
}
.deploy-stage-step.done .deploy-stage-circle { background: #2ed39a }
.deploy-stage-step.active .deploy-stage-circle { background: #3478f6 }
.deploy-stage-step.pending .deploy-stage-circle { background: #3a4759 }
.deploy-stage-name { font-weight: 600; font-size: 13px }
.deploy-stage-status { font-size: 12px; margin-top: 4px; color: #9fb0c2 }
.deploy-stage-step.done .deploy-stage-status { color: #2ed39a }
.deploy-stage-step.active .deploy-stage-status { color: #3478f6 }
.deploy-stage-step.pending .deploy-stage-status { color: #7d8fa3 }

.deploy-middle-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  align-items: stretch;
}
.deploy-schema-card,
.deploy-env-card {
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
  box-sizing: border-box;
  min-height: 230px;
}

.deploy-schema-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid #0b2534;
}
.deploy-schema-row:last-child { border-bottom: 0 }
.deploy-schema-name { font-size: 14px; color: #c9d6e3 }

.deploy-env-row {
  display: grid;
  grid-template-columns: auto 1fr auto;
  align-items: center;
  gap: 12px;
  padding: 12px 0;
  border-bottom: 1px solid #0b2534;
}
.deploy-env-row:last-child { border-bottom: 0 }
.deploy-env-badge {
  border: 1px solid;
  padding: 4px 10px;
  border-radius: 20px;
  font-weight: 700;
  font-size: 11px;
  background: #071826;
}
.deploy-env-cluster { font-weight: 600; font-size: 14px }
.deploy-env-pods { color: #9fb0c2; font-size: 13px; text-align: right }

.deploy-rollout-card {
  width: 100%;
  box-sizing: border-box;
  background: #0d1b2a;
  border: 1px solid #172a3e;
  border-radius: 12px;
  padding: 20px;
}
.deploy-rollout-desc { color: #9fb0c2; font-size: 13px; margin: 6px 0 16px }
.deploy-rollout-row {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  align-items: center;
  padding: 12px 0;
  border-bottom: 1px solid #0b2534;
}
.deploy-rollout-row:last-child { border-bottom: 0 }
.deploy-rollout-name { font-weight: 600; font-size: 14px }
.deploy-rollout-progress { color: #9fb0c2; font-size: 13px; text-align: center }

@media (max-width: 900px) {
  .deploy-stage-timeline { grid-template-columns: 1fr; row-gap: 16px }
  .deploy-stage-step {
    flex-direction: row;
    align-items: center;
    justify-content: flex-start;
    text-align: left;
    gap: 12px;
    padding: 0;
  }
  .deploy-stage-step:not(:last-child)::after { display: none }
  .deploy-stage-circle { margin-bottom: 0 }
  .deploy-middle-grid { grid-template-columns: 1fr }
  .deploy-env-row { grid-template-columns: auto 1fr; row-gap: 4px }
  .deploy-rollout-row { grid-template-columns: 1fr; row-gap: 4px }
}
</style>
