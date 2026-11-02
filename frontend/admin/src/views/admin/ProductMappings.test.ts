import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { effectScope, nextTick } from 'vue'

vi.mock('vue', async importOriginal => ({ ...await importOriginal<typeof import('vue')>(), onMounted: vi.fn() }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  getUpstreamCategoryCounts: vi.fn(), getUpstreamProducts: vi.fn(), getUpstreamCategories: vi.fn(), batchImportUpstreamProducts: vi.fn(),
  getProductMappings: vi.fn(), getProducts: vi.fn(),
} }))
vi.mock('@/utils/notify', () => ({ notifyError: vi.fn(), notifySuccess: vi.fn() }))
import ProductMappings from './ProductMappings.vue'
import { adminAPI } from '@/api/admin'

const scopes: ReturnType<typeof effectScope>[] = []
function page() {
  const scope = effectScope(); scopes.push(scope)
  return scope.run(() => (ProductMappings as any).setup({}, { expose: vi.fn() }))
}
function deferred<T = any>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const response = (id: number) => ({ data: { data: { items: [{ id, title: { 'zh-CN': `Item ${id}` }, is_active: true }], total: 2, mapped_ids: [] } } })
const flush = async () => { await nextTick(); await Promise.resolve(); await nextTick() }
beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(adminAPI.getUpstreamCategoryCounts).mockResolvedValue({ data: { data: { counts: {} } } } as any)
  vi.mocked(adminAPI.getUpstreamCategories).mockResolvedValue({ data: { data: { supported: false, categories: [] } } } as any)
  vi.mocked(adminAPI.getProductMappings).mockResolvedValue({ data: { data: { items: [], total: 0 } } } as any)
})
afterEach(() => scopes.splice(0).forEach(scope => scope.stop()))

it('discards the old supplier catalog when responses arrive out of order', async () => {
  const old = deferred(), current = deferred()
  vi.mocked(adminAPI.getUpstreamProducts).mockImplementation(({ connection_id }: any) => connection_id === '1' ? old.promise : current.promise)
  const vm = page(); vm.showImportModal.value = true
  vm.importConnectionId.value = '1'; await flush()
  vm.importConnectionId.value = '2'; await flush()
  current.resolve(response(202)); await flush()
  old.resolve(response(101)); await flush()
  expect(vm.upstreamProducts.value.map((item: any) => item.id)).toEqual([202])
})

it('does not append an old supplier page after switching connections', async () => {
  const oldPage = deferred()
  vi.mocked(adminAPI.getUpstreamProducts).mockImplementation(({ connection_id, page }: any) => {
    if (connection_id === '1' && page === 2) return oldPage.promise
    return Promise.resolve(response(connection_id === '1' ? 101 : 202) as any)
  })
  const vm = page(); vm.showImportModal.value = true
  vm.importConnectionId.value = '1'; await flush()
  const loading = vm.loadMoreUpstreamProducts()
  vm.importConnectionId.value = '2'; await flush()
  oldPage.resolve(response(102)); await loading
  expect(vm.upstreamProducts.value.map((item: any) => item.id)).toEqual([202])
  expect(vm.upstreamPage.value).toBe(1)
})

it('does not apply old category support to the newly selected supplier', async () => {
  const oldCategories = deferred()
  vi.mocked(adminAPI.getUpstreamProducts).mockResolvedValue(response(1) as any)
  vi.mocked(adminAPI.getUpstreamCategories).mockImplementation(({ connection_id }: any) => connection_id === '1'
    ? oldCategories.promise
    : Promise.resolve({ data: { data: { supported: false, categories: [] } } } as any))
  const vm = page(); vm.showImportModal.value = true
  vm.importConnectionId.value = '1'; await flush()
  vm.importConnectionId.value = '2'; await flush()
  oldCategories.resolve({ data: { data: { supported: true, categories: [{ id: 1 }] } } }); await flush()
  expect(vm.upstreamCategoriesSupported.value).toBe(false)
  expect(vm.importViewMode.value).toBe('flat')
  expect(vm.upstreamCategories.value).toEqual([])
})

it('filters mapped products in both views and restores them without losing selection', () => {
  const vm = page()
  vm.upstreamProducts.value = [
    { id: 1, category_id: 10 }, { id: 2, category_id: 10 }, { id: 3, category_id: 20 }, { id: 4 },
  ]
  vm.upstreamCategories.value = [{ id: 10, name: { 'zh-CN': 'Mixed' } }, { id: 20, name: { 'zh-CN': 'Imported' } }]
  vm.mappedUpstreamIds.value = new Set([1, 3])
  vm.selectedProductIds.value = new Set([2])
  vm.onlyUnimported.value = true
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([2, 4])
  expect(vm.productsByCategory.value.get(10).map((p: any) => p.id)).toEqual([2])
  expect(vm.categoryDisplayList.value.map((c: any) => [c.category.id, c.productCount])).toEqual([[10, 1], [0, 1]])
  vm.onlyUnimported.value = false
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([1, 2, 3, 4])
  expect([...vm.selectedProductIds.value]).toEqual([2])
})

it('can load later unimported products after a mapped-only page', async () => {
  vi.mocked(adminAPI.getUpstreamProducts)
    .mockResolvedValueOnce({ data: { data: { items: [{ id: 1 }], total: 2, mapped_ids: [1] } } } as any)
    .mockResolvedValueOnce({ data: { data: { items: [{ id: 2 }], total: 2 } } } as any)
  const vm = page(); vm.showImportModal.value = true
  vm.importConnectionId.value = '1'; await flush()
  vm.onlyUnimported.value = true
  expect(vm.visibleUpstreamProducts.value).toEqual([])
  expect(vm.hasMoreUpstream.value).toBe(true)
  expect(vm.upstreamEmptyMessage.value).toBe('productMappings.import.noUnimportedLoaded')
  await vm.loadMoreUpstreamProducts()
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([2])
  expect(vm.hasMoreUpstream.value).toBe(false)
  vm.mappedUpstreamIds.value = new Set([1, 2])
  expect(vm.upstreamEmptyMessage.value).toBe('productMappings.import.noUnimportedProducts')
})

it('hides successful imports but keeps failed ones visible for retry', async () => {
  vi.mocked(adminAPI.getUpstreamProducts).mockResolvedValue({ data: { data: { items: [{ id: 1 }, { id: 2 }], total: 2, mapped_ids: [] } } } as any)
  vi.mocked(adminAPI.batchImportUpstreamProducts).mockResolvedValue({ data: { data: {
    results: [{ upstream_product_id: 1, success: true }, { upstream_product_id: 2, success: false, error: 'Retry' }], success_count: 1,
  } } } as any)
  const vm = page(); vm.showImportModal.value = true
  vm.importConnectionId.value = '1'; await flush()
  vm.onlyUnimported.value = true
  vm.selectedProductIds.value = new Set([1, 2])
  await vm.handleBatchImport()
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([2])
  expect([...vm.selectedProductIds.value]).toEqual([2])
})

it('resets the filter when reopening the import dialog', () => {
  const vm = page()
  vm.onlyUnimported.value = true
  vm.openImportModal()
  expect(vm.onlyUnimported.value).toBe(false)
})

it('combines multilingual, ID, tag and SKU search with unimported filtering in both views', () => {
  const vm = page()
  vm.upstreamProducts.value = [
    { id: 12, title: { en: 'Cloud', 'zh-CN': '云服务' }, tags: ['hosting'], category_id: 10 },
    { id: 13, title: { en: 'Other' }, skus: [{ sku_code: 'HOSTING-PLUS' }], category_id: 10 },
    { id: 14, title: { en: 'Hosting' }, category_id: 10 },
  ]
  vm.mappedUpstreamIds.value = new Set([14])
  vm.onlyUnimported.value = true
  vm.upstreamSearch.value = ' HOSTING '
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([12, 13])
  expect(vm.productsByCategory.value.get(10).map((p: any) => p.id)).toEqual([12, 13])
  vm.upstreamSearch.value = '云'
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([12])
  vm.upstreamSearch.value = '13'
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([13])
  vm.upstreamSearch.value = 'missing'
  expect(vm.visibleUpstreamProducts.value).toEqual([])
  expect(vm.upstreamEmptyMessage.value).toBe('productMappings.import.noSearchResults')
})

it('sorts numeric prices and IDs without mutating supplier order or selection', () => {
  const vm = page()
  vm.upstreamProducts.value = [
    { id: 20, title: { en: 'Beta' }, price_amount: '10' },
    { id: 3, title: { en: 'Alpha' }, price_amount: '30', skus: [{ price_amount: '2' }] },
  ]
  vm.selectedProductIds.value = new Set([20])
  for (const [sort, ids] of [['priceAsc', [3, 20]], ['priceDesc', [20, 3]], ['idAsc', [3, 20]], ['idDesc', [20, 3]], ['nameAsc', [3, 20]], ['nameDesc', [20, 3]], ['default', [20, 3]]] as const) {
    vm.upstreamSort.value = sort
    expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual(ids)
  }
  expect(vm.upstreamProducts.value.map((p: any) => p.id)).toEqual([20, 3])
  expect([...vm.selectedProductIds.value]).toEqual([20])
})

it('searches beyond the first page and stops on a failed page without retrying forever', async () => {
  vi.mocked(adminAPI.getUpstreamProducts)
    .mockResolvedValueOnce({ data: { data: { items: [{ id: 1 }], total: 3 } } } as any)
    .mockResolvedValueOnce({ data: { data: { items: [{ id: 2, title: { en: 'Needle' } }], total: 3 } } } as any)
    .mockRejectedValueOnce(new Error('unavailable'))
  const vm = page(); vm.showImportModal.value = true
  vm.upstreamSearch.value = 'needle'
  vm.importConnectionId.value = '1'
  for (let i = 0; i < 8; i++) await flush()
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([2])
  expect(adminAPI.getUpstreamProducts).toHaveBeenCalledTimes(3)
  expect(vm.hasMoreUpstream.value).toBe(true)
})

it('blocks whole-category imports during search so hidden products are not imported', async () => {
  const vm = page(); vm.importConnectionId.value = '1'
  vm.upstreamSearch.value = 'one product'
  await vm.handleImportCategory(10)
  expect(vm.categoryImporting.value).toBe(false)
  vm.upstreamSort.value = 'priceAsc'
  vm.openImportModal()
  expect(vm.upstreamSearch.value).toBe('')
  expect(vm.upstreamSort.value).toBe('default')
})

it('continues full-catalog search after an in-flight manual page finishes', async () => {
  const second = deferred()
  vi.mocked(adminAPI.getUpstreamProducts)
    .mockResolvedValueOnce({ data: { data: { items: [{ id: 1 }], total: 3 } } } as any)
    .mockImplementationOnce(() => second.promise)
    .mockResolvedValueOnce({ data: { data: { items: [{ id: 3, title: { en: 'Needle' } }], total: 3 } } } as any)
  const vm = page(); vm.showImportModal.value = true
  vm.importConnectionId.value = '1'; await flush()
  const manual = vm.loadMoreUpstreamProducts()
  vm.upstreamSearch.value = 'needle'; await flush()
  second.resolve({ data: { data: { items: [{ id: 2 }], total: 3 } } })
  await manual
  expect(adminAPI.getUpstreamProducts).toHaveBeenCalledTimes(3)
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([3])
})

it('handles null translations and preserves hidden selections when toggling search-result select-all', () => {
  const vm = page()
  vm.upstreamProducts.value = [
    { id: 1, title: { en: 'Other' } },
    { id: 2, title: { 'zh-CN': null, en: 'Cloud' } },
    { id: 3, title: { en: 'Cloud Plus' } },
  ]
  vm.selectedProductIds.value = new Set([1])
  vm.upstreamSearch.value = 'cloud'
  expect(vm.visibleUpstreamProducts.value.map((p: any) => p.id)).toEqual([2, 3])
  vm.toggleSelectAll()
  expect([...vm.selectedProductIds.value]).toEqual([1, 2, 3])
  vm.toggleSelectAll()
  expect([...vm.selectedProductIds.value]).toEqual([1])
})
