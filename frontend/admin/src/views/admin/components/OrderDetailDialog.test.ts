import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'

vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, locale: ref('zh-CN') }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { manualConfirmOrderPayment: vi.fn(), getOrder: vi.fn(), getProcurementOrderByLocalOrder: vi.fn() } }))
vi.mock('@/utils/confirm', () => ({ confirmAction: vi.fn() }))
import OrderDetailDialog from './OrderDetailDialog.vue'
import { adminAPI } from '@/api/admin'
import { confirmAction } from '@/utils/confirm'

const scopes: ReturnType<typeof effectScope>[] = []
const order = (id = 10) => ({ id, order_no: `TEST-${id}`, status: 'pending_payment', items: [], children: [], payments: [{ id: 20, status: 'pending' }] })
function dialog() {
  const scope = effectScope(); scopes.push(scope)
  const vm = scope.run(() => (OrderDetailDialog as any).setup({ modelValue: true, order: null, siteCurrency: 'CNY', maxRefundDays: 30 }, { expose: vi.fn(), emit: vi.fn() }))
  vm.selectedOrder.value = order(); vm.manualConfirmPaymentId.value = 20
  return vm
}
beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(confirmAction).mockResolvedValue(true)
  vi.mocked(adminAPI.manualConfirmOrderPayment).mockResolvedValue({ data: { data: {} } } as any)
  vi.mocked(adminAPI.getOrder).mockResolvedValue({ data: { data: order() } } as any)
})
afterEach(() => scopes.splice(0).forEach(scope => scope.stop()))

it('passes the selected no-fulfillment mode with the payment record', async () => {
  const vm = dialog(); await vm.submitManualConfirm(true)
  expect(adminAPI.manualConfirmOrderPayment).toHaveBeenCalledWith(10, expect.objectContaining({ payment_id: 20, hold_fulfillment: true }))
  expect(vm.manualConfirmSuccess.value).toBe('admin.orders.manualConfirmHeldSuccess')
})
it('offers release only for paid orders with a held payment', async () => {
  const vm = dialog()
  vm.selectedOrder.value = { ...order(), status: 'paid', paid_at: '2026-01-01', payments: [{ id: 20, manual_fulfillment_held: true }] }
  expect(vm.canManualConfirmPayment(vm.selectedOrder.value)).toBe(true)
  await vm.submitManualConfirm()
  expect(adminAPI.manualConfirmOrderPayment).toHaveBeenCalledWith(10, expect.objectContaining({ hold_fulfillment: false }))
  expect(vm.canManualConfirmPayment({ ...order(), status: 'paid', paid_at: '2026-01-01' })).toBe(false)
})
it('does not confirm another order after switching during the confirmation dialog', async () => {
  let resolve!: (confirmed: boolean) => void
  vi.mocked(confirmAction).mockReturnValue(new Promise(done => { resolve = done }))
  const vm = dialog(); const pending = vm.submitManualConfirm(true)
  vm.selectedOrder.value = order(11); vm.resetManualConfirmForm()
  resolve(true); await pending
  expect(adminAPI.manualConfirmOrderPayment).not.toHaveBeenCalled()
})
it('blocks repeated clicks while awaiting confirmation and respects cancellation', async () => {
  let resolve!: (confirmed: boolean) => void
  vi.mocked(confirmAction).mockReturnValue(new Promise(done => { resolve = done }))
  const vm = dialog(); const pending = vm.submitManualConfirm(true)
  await vm.submitManualConfirm()
  expect(confirmAction).toHaveBeenCalledTimes(1)
  resolve(false); await pending
  expect(adminAPI.manualConfirmOrderPayment).not.toHaveBeenCalled()
  expect(vm.manualConfirmSubmitting.value).toBe(false)
})
