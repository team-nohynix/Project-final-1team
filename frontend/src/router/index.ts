import { createRouter, createWebHistory } from 'vue-router'
import DefaultLayout from '../components/DefaultLayout.vue'
import DashboardView from '../views/DashboardView.vue'
import OrderManagementView from '../views/OrderManagementView.vue'
import MatchingEngineView from '../views/MatchingEngineView.vue'
import MarketOrderBookView from '../views/MarketOrderBookView.vue'
import LoadTestReplayView from '../views/LoadTestReplayView.vue'
import AITraderView from '../views/AITraderView.vue'
import RealtimeMonitoringView from '../views/RealtimeMonitoringView.vue'
import TestResultTrackingView from '../views/TestResultTrackingView.vue'
import FaultRecoveryView from '../views/FaultRecoveryView.vue'
import MarketStreamView from '../views/MarketStreamView.vue'
import DeploymentOpsView from '../views/DeploymentOpsView.vue'

const routes = [
  {
    path: '/',
    component: DefaultLayout,
    children: [
      { path: '', name: 'dashboard', component: DashboardView },
      { path: 'orders', name: 'order-management', component: OrderManagementView },
      { path: 'matching-engine', name: 'matching-engine', component: MatchingEngineView },
      { path: 'market-orderbook', name: 'market-orderbook', component: MarketOrderBookView },
      { path: 'load-test/replay', name: 'load-test-replay', component: LoadTestReplayView },
      { path: 'load-test/ai-trader', name: 'ai-trader', component: AITraderView },
      { path: 'monitoring', name: 'monitoring', component: RealtimeMonitoringView },
      { path: 'test-results', name: 'test-results', component: TestResultTrackingView },
      { path: 'fault-recovery', name: 'fault-recovery', component: FaultRecoveryView },
      { path: 'market-stream', name: 'market-stream', component: MarketStreamView, meta: { keepAlive: true } },
      { path: 'deployment-ops', name: 'deployment-ops', component: DeploymentOpsView },
    ],
  },
]

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
})

export default router
