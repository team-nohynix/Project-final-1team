<script setup>
import { ref, computed, watch } from 'vue'

const markets = ['BTC/KRW', 'ETH/KRW', 'XRP/KRW']

const form = ref({
  market: markets[0],
  side: 'BUY',
  type: 'LIMIT',
  price: 50000000,
  quantity: 0.001,
  idempotencyKey: '',
})

// Dummy recent orders
const recentOrders = ref([
  {
    id: 'ORD-1001',
    market: 'BTC/KRW',
    side: 'BUY',
    type: 'LIMIT',
    price: 48500000,
    quantity: 0.002,
    idempotencyKey: 'abc-123',
    status: 'ACCEPTED',
    partition: 1,
    offset: 124,
  },
  {
    id: 'ORD-1002',
    market: 'BTC/KRW',
    side: 'SELL',
    type: 'LIMIT',
    price: 49000000,
    quantity: 0.0015,
    idempotencyKey: 'def-456',
    status: 'OPEN',
    partition: 2,
    offset: 128,
  },
])

const statusColor = (s) => {
  switch (s) {
    case 'ACCEPTED':
      return '#2ed39a'
    case 'PARTIAL':
      return '#ffb84d'
    case 'OPEN':
      return '#3478f6'
    case 'CANCELLED':
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
  if (!form.value.price || form.value.price <= 0) errs.push('Price must be > 0')
  if (!form.value.quantity || form.value.quantity <= 0) errs.push('Quantity must be > 0')
  validation.value.errors = errs
  validation.value.valid = errs.length === 0
  return validation.value.valid
}

const generateId = () => `ORD-${Math.floor(1000 + Math.random() * 9000)}`

// keep existing business logic intact
const submitTestOrder = () => {
  if (!validate()) return

  // Idempotency: if a matching idempotencyKey exists, return same response
  const existing = recentOrders.value.find((o) => o.idempotencyKey === form.value.idempotencyKey)
  if (existing) {
    // mark duplicate visually
    existing.status = 'DUPLICATE'
    return
  }

  const order = {
    id: generateId(),
    market: form.value.market,
    side: form.value.side,
    type: form.value.type,
    price: Number(form.value.price),
    quantity: Number(form.value.quantity),
    idempotencyKey: form.value.idempotencyKey,
    status: 'ACCEPTED',
    partition: Math.floor(Math.random() * 3),
    offset: Math.floor(Math.random() * 1000),
  }

  recentOrders.value.unshift(order)
}

const cancelOrder = (id) => {
  const o = recentOrders.value.find((r) => r.id === id)
  if (!o) return
  o.status = 'CANCELLED'
  if (o.partition === undefined) o.partition = 0
  if (o.offset === undefined) o.offset = Math.floor(Math.random() * 1000)
}

// UI helpers
const isFormReady = computed(() => {
  return (
    form.value.idempotencyKey &&
    form.value.idempotencyKey.trim() !== '' &&
    form.value.price > 0 &&
    form.value.quantity > 0
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
  // show cancel only for ACCEPTED, OPEN, PARTIAL
  const cancelable = ['ACCEPTED', 'OPEN', 'PARTIAL']
  return cancelable.includes(o.status)
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
            <span class="field-label">주문 유형</span>
            <input class="field-input" readonly value="LIMIT" />
          </label>

          <label class="field">
            <span class="field-label">가격</span>
            <input class="field-input" type="number" v-model.number="form.price" />
          </label>

          <label class="field">
            <span class="field-label">수량</span>
            <input class="field-input" type="number" step="0.0001" v-model.number="form.quantity" />
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
              단건 테스트 주문 전송
            </button>
          </div>

          <div class="validation">
            <div v-if="!validation.valid && validation.errors.length" class="errors">
              <div v-for="e in validation.errors" :key="e">• {{ e }}</div>
            </div>
            <div v-else-if="validation.valid" class="ok">입력값 검증 완료</div>
          </div>
        </div>
      </article>

      <article class="panel right-panel status-panel">
        <h3>최근 접수 주문</h3>

        <div class="recent-list">
          <div v-for="o in recentOrders" :key="o.id" class="order-card">
            <div class="order-left">
              <div class="order-id">{{ o.id }}</div>
              <div class="order-meta">{{ o.market }} • {{ o.side }}</div>
              <div class="order-idemp">Idempotency: {{ o.idempotencyKey || '-' }}</div>
            </div>

            <div class="order-right">
              <div class="status-value">
                <i :style="{ backgroundColor: statusColor(o.status) }" class="status-dot"></i>
                <span>{{ o.status }}</span>
              </div>
              <div class="order-offset">
                PK {{ o.partition ?? '-' }} · Off {{ o.offset ?? '-' }}
              </div>
              <div>
                <button v-if="canCancel(o)" @click="cancelOrder(o.id)" class="cancel-small">
                  취소
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- removed dedicated cancel card per requirements -->

        <div class="idempotent-card">동일 주문 번호는 같은 응답을 반환합니다 (Idempotent)</div>
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
