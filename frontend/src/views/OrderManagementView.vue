<script setup>
import { ref, computed, watch } from 'vue'

// Use the canonical 20 KRW markets defined in backend/upbit/markets.go
const markets = [
  'KRW-USDT',
  'KRW-BTC',
  'KRW-XRP',
  'KRW-ETH',
  'KRW-ONDO',
  'KRW-LA',
  'KRW-SHIB',
  'KRW-RE',
  'KRW-DOGE',
  'KRW-SLX',
  'KRW-KAITO',
  'KRW-SOL',
  'KRW-XLM',
  'KRW-WLD',
  'KRW-MIRA',
  'KRW-ERA',
  'KRW-ADA',
  'KRW-AI',
  'KRW-NEAR',
  'KRW-ARX',
]

const form = ref({
  market: markets[0],
  side: 'BUY',
  price: '',
  quantity: '',
  idempotencyKey: '',
})

// displayPrice holds the formatted string shown in the input (with commas)
import { ref as _ref } from 'vue'
const displayPrice = ref('')

const addCommas = (s) => {
  if (!s) return ''
  return s.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

const onPriceInput = (e) => {
  const v = e.target.value
  // remove any non-digit characters
  const digits = String(v).replace(/[^0-9]/g, '')
  form.value.price = digits
  displayPrice.value = digits ? addCommas(digits) : ''
}

const priceLabel = computed(() => (form.value.side === 'BUY' ? '매수 가격 (KRW)' : '매도 가격 (KRW)'))
const pricePlaceholder = computed(() => (form.value.side === 'BUY' ? '매수 희망 가격을 입력하세요' : '매도 희망 가격을 입력하세요'))

const quantityUnit = computed(() => {
  try {
    const m = String(form.value.market || '')
    const parts = m.split('-')
    return parts.length > 1 ? parts[1] : ''
  } catch (e) {
    return ''
  }
})

const quantityLabel = computed(() => `수량${quantityUnit.value ? ` (${quantityUnit.value})` : ''}`)
const quantityPlaceholder = ref('주문 수량을 입력하세요')

const onQuantityInput = (e) => {
  const v = e.target.value
  // allow digits and at most one decimal point
  let sanitized = String(v).replace(/[^0-9.]/g, '')
  const parts = sanitized.split('.')
  if (parts.length > 2) {
    sanitized = parts[0] + '.' + parts.slice(1).join('')
  }
  form.value.quantity = sanitized
  // ensure the input shows the sanitized value
  e.target.value = sanitized
}

// Session storage key for recent orders (per-tab session scope)
const STORAGE_KEY = 'order_mgmt_recent_orders'

const loadRecentOrders = () => {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed
  } catch (e) {
    // malformed JSON or sessionStorage access denied — start with empty list
    return []
  }
}

const saveRecentOrders = () => {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(recentOrders.value))
  } catch (e) {
    // ignore storage errors (e.g., quota), keep in-memory list as source of truth
  }
}

// Recent orders for this browser session only (restored from sessionStorage)
const recentOrders = ref(loadRecentOrders())

// UI state
const isSubmitting = ref(false)
const infoMessage = ref('')
const errorMessage = ref('')

const statusColor = (s) => {
  switch (s) {
    case 'ACCEPTED':
      return '#2ed39a'
    case 'PARTIALLY_FILLED':
      return '#ffb84d'
    case 'FILLED':
      return '#3478f6'
    case 'CANCELED':
      return '#ff6b6b'
    case 'DUPLICATE':
      return '#9b7bff'
    default:
      return '#7a8a99'
  }
}

const validation = ref({ valid: false, errors: [] })

const validate = () => {
  const errs = []
  if (!form.value.idempotencyKey || form.value.idempotencyKey.trim() === '')
    errs.push('Idempotency Key required')
  if (!form.value.price || Number(form.value.price) <= 0) errs.push('Price must be > 0')
  if (!form.value.quantity || Number(form.value.quantity) <= 0) errs.push('Quantity must be > 0')
  validation.value.errors = errs
  validation.value.valid = errs.length === 0
  return validation.value.valid
}

// Submit a real order to backend via proxy /order-api/v1/orders
const submitTestOrder = async () => {
  infoMessage.value = ''
  errorMessage.value = ''
  if (!validate()) return
  if (isSubmitting.value) return

  // ensure form values normalized
  const idemp = String(form.value.idempotencyKey).trim()
  const market = String(form.value.market)

  const side = form.value.side === 'BUY' ? 'BUY' : 'SELL'

  // price/quantity: ensure string, remove any commas
  const priceStr = String(form.value.price).replace(/,/g, '')
  const qtyStr = String(form.value.quantity).replace(/,/g, '')

  // Idempotency check: if we already have an entry with same key, do not re-add duplicate
  const existing = recentOrders.value.find((o) => o.idempotencyKey === idemp)
  if (existing) {
    // If existing is canceled, do not revert it; show info and return
    infoMessage.value = '동일한 중복 방지 키로 기존 주문 응답이 반환되었습니다.'
    return
  }

  isSubmitting.value = true

  try {
    const resp = await fetch('/order-api/v1/orders', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Idempotency-Key': idemp,
        'X-Order-Mode': 'PAPER_TRADING',
      },
      body: JSON.stringify({ market, side, price: priceStr, quantity: qtyStr }),
    })

    if (!resp.ok) {
      let bodyText = ''
      try {
        const j = await resp.json()
        bodyText = j?.error || JSON.stringify(j)
      } catch (e) {
        bodyText = resp.statusText || `HTTP ${resp.status}`
      }
      throw new Error(bodyText)
    }

    const data = await resp.json()

    // If an existing order with same idempotencyKey exists, consider duplicate handling
    const already = recentOrders.value.find((o) => o.idempotencyKey === idemp)
    if (already) {
      if (already.id === data.orderId) {
        infoMessage.value = '동일한 중복 방지 키로 기존 주문 응답이 반환되었습니다.'
      }
      // do not overwrite a canceled existing order
      if (already.status !== 'CANCELED') {
        // update existing entry with latest data
        already.id = data.orderId
        already.market = data.market
        already.side = data.side
        already.price = data.price
        already.quantity = data.quantity
        already.status = data.status
        already.acceptedAt = data.acceptedAt
        infoMessage.value = '주문이 접수되었습니다.'
      }
    } else {
      // add new order to session list
      recentOrders.value.unshift({
        id: data.orderId,
        market: data.market,
        side: data.side,
        price: data.price,
        quantity: data.quantity,
        idempotencyKey: idemp,
        status: data.status,
        acceptedAt: data.acceptedAt,
      })
      infoMessage.value = '주문이 접수되었습니다.'
    }
    // persist to sessionStorage
    saveRecentOrders()
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    errorMessage.value = msg || '주문 접수 중 오류가 발생했습니다.'
  } finally {
    isSubmitting.value = false
  }
}

const cancelOrder = async (id) => {
  errorMessage.value = ''
  infoMessage.value = ''
  const o = recentOrders.value.find((r) => r.id === id)
  if (!o) return
  if (!confirm('정말로 주문을 취소하시겠습니까?')) return

  try {
    const resp = await fetch(`/order-api/v1/orders/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    })

    if (!resp.ok) {
      let bodyText = ''
      try {
        const j = await resp.json()
        bodyText = j?.error || JSON.stringify(j)
      } catch (e) {
        bodyText = resp.statusText || `HTTP ${resp.status}`
      }
      throw new Error(bodyText)
    }

    const data = await resp.json()
    // update order entry
    o.status = data.status || 'CANCELED'
    o.canceledAt = data.canceledAt
    o.canceledQuantity = data.canceledQuantity
    infoMessage.value = '주문이 취소되었습니다.'
    // persist updated status to sessionStorage
    saveRecentOrders()
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    errorMessage.value = msg || '주문 취소 중 오류가 발생했습니다.'
  }
}

// UI helpers
const isFormReady = computed(() => {
  return (
    form.value.idempotencyKey &&
    form.value.idempotencyKey.trim() !== '' &&
    Number(form.value.price) > 0 &&
    Number(form.value.quantity) > 0 &&
    !isSubmitting.value
  )
})

// Clear validation errors immediately when idempotency key is provided
watch(
  () => form.value.idempotencyKey,
  (val) => {
    if (val && val.trim() !== '') {
      validation.value.errors = validation.value.errors.filter((e) => !e.includes('Idempotency'))
      validation.value.valid = validation.value.errors.length === 0
    }
  },
)

// helper to determine whether an order can be cancelled
const canCancel = (o) => {
  // cancel allowed for ACCEPTED or PARTIALLY_FILLED
  const cancelable = ['ACCEPTED', 'PARTIALLY_FILLED']
  return cancelable.includes(o.status)
}

// Presentation helpers (only affect UI display)
const formatPrice = (v) => {
  try {
    if (v === null || v === undefined || v === '') return '-'
    const n = Number(String(v).replace(/,/g, ''))
    if (!Number.isFinite(n)) return String(v)
    return n.toLocaleString('ko-KR') + ' KRW'
  } catch (e) {
    return String(v)
  }
}

const formatKst = (iso) => {
  try {
    if (!iso) return '-'
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return '-'
    const s = d.toLocaleString('ko-KR', {
      timeZone: 'Asia/Seoul',
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
    })
    return `${s} KST`
  } catch (e) {
    return iso || '-'
  }
}
</script>

<template>
  <div class="order-management-view">
    <header class="page-header">
      <div>
        <h2>주문 API 검증</h2>
        <p>주문 접수·유효성 검증·중복 방지·Kafka 발행을 단건으로 확인합니다.</p>
      </div>
    </header>

    <section class="dashboard-grid">
      <article class="panel left-panel">
        <h3>단건 주문 테스트</h3>
        <div class="card-note">
          실제 거래 주문이 아닌 주문 API의 접수·중복 방지·취소 동작을 검증하기 위한 기능입니다.
        </div>

        <div class="form-column">
          <label class="field">
            <span class="field-label">마켓</span>
            <select v-model="form.market" class="field-input">
              <option v-for="m in markets" :key="m" :value="m">{{ m }}</option>
            </select>
          </label>

          <label class="field">
            <span class="field-label">매수/매도</span>
            <select v-model="form.side" class="field-input">
              <option value="BUY">매수</option>
              <option value="SELL">매도</option>
            </select>
          </label>



          <label class="field">
            <span class="field-label">{{ priceLabel }}</span>
            <input
              class="field-input"
              type="text"
              v-model="displayPrice"
              @input="onPriceInput"
              :placeholder="pricePlaceholder"
            />
          </label>

          <label class="field">
            <span class="field-label">{{ quantityLabel }}</span>
            <input
              class="field-input"
              type="text"
              v-model="form.quantity"
              @input="onQuantityInput"
              :placeholder="quantityPlaceholder"
            />
          </label>

          <label class="field">
            <span class="field-label">중복 방지 키 (Idempotency Key)</span>
            <input class="field-input" v-model="form.idempotencyKey" />
          </label>

          <div class="actions">
            <button
              type="button"
              :disabled="!isFormReady"
              :class="{ 'btn-enabled': isFormReady, 'btn-disabled': !isFormReady }"
              @click="submitTestOrder"
            >
              <span v-if="!isSubmitting">단건 테스트 주문 전송</span>
              <span v-else>전송 중...</span>
            </button>
          </div>

          <div class="validation">
            <div v-if="!validation.valid && validation.errors.length" class="errors">
              <div v-for="e in validation.errors" :key="e">• {{ e }}</div>
            </div>
            <div v-else-if="validation.valid" class="ok">입력값 검증 완료</div>
          </div>

          <div v-if="errorMessage" class="errors">{{ errorMessage }}</div>
          <div v-if="infoMessage" class="ok">{{ infoMessage }}</div>
        </div>
      </article>

      <article class="panel right-panel status-panel">
        <h3>최근 접수 주문</h3>

        <div class="note">현재 브라우저 세션에서 접수한 주문</div>
        <div class="recent-list">
          <div v-for="o in recentOrders" :key="o.id" class="order-card">
            <div class="order-left">
              <div class="order-id">{{ o.id }}</div>
              <div class="order-meta">{{ o.market }} • {{ o.side }}</div>
              <div class="order-idemp">Idempotency: {{ o.idempotencyKey || '-' }}</div>
              <div class="order-meta">가격: {{ formatPrice(o.price) }} · 수량: {{ o.quantity }}</div>
              <div class="order-meta">접수시각: {{ formatKst(o.acceptedAt) }}</div>
              <div v-if="o.status === 'CANCELED'" class="order-meta">취소시각: {{ formatKst(o.canceledAt) }} · 취소수량: {{ o.canceledQuantity }}</div>
            </div>

            <div class="order-right">
              <div class="status-value">
                <i :style="{ backgroundColor: statusColor(o.status) }" class="status-dot"></i>
                <span>{{ o.status }}</span>
              </div>
              <div>
                <button v-if="canCancel(o)" @click="cancelOrder(o.id)" class="cancel-small">
                  취소
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="idempotent-card">
          동일한 중복 방지 키로 재요청하면 Redis에 저장된 기존 주문 응답이 반환됩니다.
          <br />
          최근 주문 목록은 현재 브라우저 세션에서만 유지됩니다.
        </div>
      </article>
    </section>
  </div>
</template>

<style scoped>
.dashboard-grid {
  display: flex;
  gap: 20px;
  margin-top: 12px;
  align-items: flex-start;
}

.left-panel {
  flex: 0 0 38%;
}

.right-panel {
  flex: 1 1 62%;
}

/* stack vertically on narrow screens */
@media (max-width: 1100px) {
  .dashboard-grid {
    flex-direction: column;
  }
  .left-panel,
  .right-panel {
    flex: 1 1 auto;
  }
}

.form-column {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin-top: 12px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.field-label {
  font-size: 13px;
  color: #9fb0c1;
}

.field-input {
  height: 44px;
  padding: 10px 12px;
  border-radius: 10px;
  border: 1px solid #20344b;
  background: #071624;
  color: #f3f7fc;
}

.field-input::placeholder {
  color: #71869c;
}

.actions {
  margin-top: 8px;
}

.btn-enabled {
  width: 100%;
  background: linear-gradient(180deg, #3fa5ff, #358ef0);
  color: #fff;
  font-weight: 600;
  border: 0;
  height: 48px;
  border-radius: 10px;
  cursor: pointer;
}
.btn-enabled:hover {
  background: linear-gradient(180deg, #57b8ff, #3fa5ff);
}
.btn-disabled {
  width: 100%;
  background: #10243a;
  color: #8b9bb0;
  border: 0;
  height: 48px;
  border-radius: 10px;
  cursor: not-allowed;
}

.validation {
  margin-top: 8px;
}
.errors {
  color: #ff6b6b;
}
.ok {
  color: #2ed39a;
  font-weight: 700;
}

.recent-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 12px;
}

.order-card {
  display: flex;
  justify-content: space-between;
  background: #071824;
  padding: 14px;
  border-radius: 10px;
  border: 1px solid #173141;
  align-items: center;
}
.order-left {
  display: flex;
  flex-direction: column;
}
.order-id {
  font-weight: 700;
}
.order-meta {
  color: #9fb0c1;
  font-size: 13px;
  margin-top: 6px;
}
.order-idemp {
  color: #71869c;
  font-size: 12px;
  margin-top: 6px;
}
.order-right {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 8px;
}
.status-value {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 700;
}
.status-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  display: inline-block;
}
.order-offset {
  color: #71869c;
  font-size: 12px;
}
.cancel-small {
  padding: 6px 10px;
  border-radius: 8px;
  background: #11243a;
  color: #f3f7fc;
  border: 0;
}

/* dedicated cancel card removed */

.idempotent-card {
  margin-top: 12px;
  background: #071824;
  border: 1px solid #173141;
  border-radius: 10px;
  padding: 12px;
  color: #9fb0c1;
}
</style>
