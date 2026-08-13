import { createRouter, createWebHistory } from 'vue-router'
import DefaultLayout from '../components/DefaultLayout.vue'
import DashboardView from '../views/DashboardView.vue'
import MatchingEngineView from '../views/MatchingEngineView.vue'
import MarketOrderBookView from '../views/MarketOrderBookView.vue'
import LoadTestReplayView from '../views/LoadTestReplayView.vue'
import AITraderView from '../views/AITraderView.vue'
import TestResultTrackingView from '../views/TestResultTrackingView.vue'
import MarketStreamView from '../views/MarketStreamView.vue'

const routes = [
  {
    path: '/',
    component: DefaultLayout,
    children: [
      { path: '', name: 'dashboard', component: DashboardView },
      { path: 'matching-engine', name: 'matching-engine', component: MatchingEngineView },
      { path: 'market-orderbook', name: 'market-orderbook', component: MarketOrderBookView },
      { path: 'load-test/replay', name: 'load-test-replay', component: LoadTestReplayView },
      { path: 'load-test/ai-trader', name: 'ai-trader', component: AITraderView },
      { path: 'test-results', name: 'test-results', component: TestResultTrackingView },
      { path: 'market-stream', name: 'market-stream', component: MarketStreamView, meta: { keepAlive: true } },
      
    ],
  },
]

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
})

export default router
