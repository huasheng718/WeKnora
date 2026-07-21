import assert from 'node:assert/strict'
import test from 'node:test'

const values = new Map<string, string>()
globalThis.window = {
  __RUNTIME_CONFIG__: {},
  location: { pathname: '/', href: '' },
} as unknown as Window & typeof globalThis
globalThis.document = {
  addEventListener() {},
  removeEventListener() {},
  createElement: () => ({ content: {}, style: {}, setAttribute() {} }),
  getElementById: () => null,
  documentElement: { setAttribute() {}, style: { setProperty() {} } },
} as unknown as Document
globalThis.localStorage = {
  getItem: key => values.get(key) ?? null,
  setItem: (key, value) => values.set(key, value),
  removeItem: key => values.delete(key),
  clear: () => values.clear(),
  key: index => [...values.keys()][index] ?? null,
  get length() { return values.size },
} as Storage

const { createPinia, setActivePinia } = await import('pinia')
const { useProductionStore } = await import('./production')
const { useAuthStore } = await import('./auth')

function project(id: string, name = id) {
  return {
    id, tenant_id: 7, name, description: '', owner_user_id: 'user-1', status: 'active' as const,
    created_at: '', updated_at: '', deleted_at: null,
  }
}

function document(id: string) {
  return {
    id, tenant_id: 7, project_id: 'project-1', document_type_id: 'type-1', document_type_schema_version: 1,
    title: id, status: 'draft' as const, created_by: 'user-1', created_at: '', updated_at: '',
  }
}

test('production replacements clone input entities and clear invalid active ids', () => {
  setActivePinia(createPinia())
  const store = useProductionStore()
  const projects = [project('project-1', 'Original')]

  store.replaceProjects(projects)
  store.activeProjectId = 'project-1'
  projects[0].name = 'Mutated outside store'
  assert.equal(store.projectsById['project-1'].name, 'Original')

  store.replaceProjects([])
  assert.equal(store.activeProjectId, null)

  store.replaceDocuments([document('document-1')])
  store.activeDocumentId = 'document-1'
  store.replaceDocuments([])
  assert.equal(store.activeDocumentId, null)
})

test('production reset clears every collection, loading, error, and active selection', () => {
  setActivePinia(createPinia())
  const store = useProductionStore()
  store.replaceProjects([project('project-1')])
  store.replaceDocuments([document('document-1')])
  store.activeProjectId = 'project-1'
  store.activeDocumentId = 'document-1'
  store.setLoading('projects', true)
  store.setError(new Error('failed'))

  store.reset()

  for (const collection of [
    store.projectsById, store.documentTypesById, store.sourceSetsById, store.sourceItemsById,
    store.documentsById, store.versionsById, store.runsById, store.reviewsById, store.releaseTargetsById,
  ]) assert.deepEqual(collection, {})
  assert.deepEqual(store.loadingByKey, {})
  assert.equal(store.lastError, null)
  assert.equal(store.activeProjectId, null)
  assert.equal(store.activeDocumentId, null)
  assert.equal(store.activeVersionId, null)
})

test('tenant switching and logout clear production state', () => {
  setActivePinia(createPinia())
  const production = useProductionStore()
  const auth = useAuthStore()

  auth.setSelectedTenant(7, 'Tenant 7')
  production.replaceDocuments([document('document-1')])
  auth.setSelectedTenant(8, 'Tenant 8')
  assert.deepEqual(production.documentsById, {})

  production.replaceProjects([project('project-1')])
  auth.logout()
  assert.deepEqual(production.projectsById, {})
})
