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

function version(id: string) {
  return {
    id, document_id: 'document-1', tenant_id: 7, project_id: 'project-1', version_number: 1,
    source_set_id: 'source-set-1', origin: 'human' as const, change_summary: '', content_digest: 'digest',
    created_by: 'user-1', created_at: '',
    blocks: [{
      id: 'block-1', version_id: id, logical_block_id: 'logical-1', block_type: 'paragraph', position: 0,
      content: { text: 'Original block' }, attributes: {}, evidence_refs: [], ai_provenance: {}, content_digest: 'block-digest',
    }],
    lineage: [{
      id: 'lineage-1', from_version_id: 'version-0', from_logical_block_id: 'logical-0',
      to_version_id: id, to_logical_block_id: 'logical-1', relation: 'same' as 'same' | 'split' | 'merged',
    }],
  }
}

function review(id: string) {
  return {
    id, tenant_id: 7, project_id: 'project-1', document_id: 'document-1', version_id: 'version-1',
    policy_snapshot: { required: ['business_reviewer'] }, policy_digest: 'policy-digest', status: 'pending' as const,
    submitted_by: 'user-1', submitted_at: '', created_at: '', updated_at: '',
    steps: [{
      id: 'step-1', review_request_id: id, tenant_id: 7, project_id: 'project-1', document_id: 'document-1',
      version_id: 'version-1', required_role: 'business_reviewer' as const, sequence: 1,
      decision: 'pending' as const, comment: 'Original review', created_at: '', updated_at: '',
    }],
  }
}

function releaseTarget(id: string) {
  return {
    id, release_id: 'release-1', tenant_id: 7, project_id: 'project-1', document_id: 'document-1',
    version_id: 'version-1', target_knowledge_base_id: 'kb-1', knowledge_id: 'knowledge-1',
    release_digest: 'release-digest', config_snapshot: { mode: { name: 'Original config' } },
    config_digest: 'config-digest', status: 'ready' as const, retention_days: 30, created_at: '', updated_at: '',
  }
}

test('production replacements clone input entities and cascade invalid active ids', () => {
  setActivePinia(createPinia())
  const store = useProductionStore()
  const projects = [project('project-1', 'Original')]

  store.replaceProjects(projects)
  store.activeProjectId = 'project-1'
  store.activeDocumentId = 'document-1'
  store.activeVersionId = 'version-1'
  projects[0].name = 'Mutated outside store'
  assert.equal(store.projectsById['project-1'].name, 'Original')

  store.replaceProjects([])
  assert.equal(store.activeProjectId, null)
  assert.equal(store.activeDocumentId, null)
  assert.equal(store.activeVersionId, null)

  store.replaceDocuments([document('document-1')])
  store.activeDocumentId = 'document-1'
  store.activeVersionId = 'version-1'
  store.replaceDocuments([])
  assert.equal(store.activeDocumentId, null)
  assert.equal(store.activeVersionId, null)
})

test('production replacements deep-snapshot versions, reviews, and release configuration', () => {
  setActivePinia(createPinia())
  const store = useProductionStore()
  const versions = [version('version-1')]
  const reviews = [review('review-1')]
  const targets = [releaseTarget('target-1')]

  store.replaceVersions(versions)
  store.replaceReviews(reviews)
  store.replaceReleaseTargets(targets)
  versions[0].blocks[0].content.text = 'Mutated block'
  versions[0].lineage[0].relation = 'split'
  reviews[0].steps[0].comment = 'Mutated review'
  reviews[0].policy_snapshot.required[0] = 'publisher'
  targets[0].config_snapshot.mode.name = 'Mutated config'

  assert.deepEqual(store.versionsById['version-1'].blocks[0].content, { text: 'Original block' })
  assert.equal(store.versionsById['version-1'].lineage?.[0].relation, 'same')
  assert.equal(store.reviewsById['review-1'].steps?.[0].comment, 'Original review')
  assert.deepEqual(store.reviewsById['review-1'].policy_snapshot, { required: ['business_reviewer'] })
  assert.deepEqual(store.releaseTargetsById['target-1'].config_snapshot, { mode: { name: 'Original config' } })
})

test('production upserts deep-snapshot every entity category', () => {
  setActivePinia(createPinia())
  const store = useProductionStore()
  const sourceSet = {
    id: 'source-set-1', tenant_id: 7, project_id: 'project-1', document_type_id: 'type-1', status: 'ready' as const,
    created_by: 'user-1', created_at: '', external: { value: 'Original source set' },
  }
  const sourceItem = {
    id: 'source-item-1', source_set_id: 'source-set-1', source_kind: 'manual' as const, source_system: 'manual',
    external_id: 'external-1', title: 'Source', mime_type: 'text/plain', content_digest: 'digest', captured_at: '',
    metadata: { label: 'Original metadata' }, status: 'accepted' as const, created_at: '',
    external: { value: 'Original source item' },
  }
  const run = {
    id: 'run-1', tenant_id: 7, project_id: 'project-1', source_set_id: 'source-set-1', run_type: 'write' as const,
    status: 'running' as const, attempt: 1, current_step: 1, wakeup_version: 1, wakeup_enqueued_version: 1,
    state_payload: { stage: { name: 'Original run' } }, model_id: 'model-1', document_type_snapshot: {},
    workflow_plan_snapshot: {}, workflow_plan_digest: 'digest', idempotency_key: 'command-1', created_at: '', updated_at: '',
    external: { value: 'Original run entity' },
  }
  const entities = {
    project: { ...project('project-1'), external: { value: 'Original project' } },
    sourceSet,
    sourceItem,
    document: { ...document('document-1'), external: { value: 'Original document' } },
    version: { ...version('version-1'), external: { value: 'Original version' } },
    run,
    review: { ...review('review-1'), external: { value: 'Original review entity' } },
    target: { ...releaseTarget('target-1'), external: { value: 'Original target' } },
  }

  store.upsertProject(entities.project)
  store.upsertSourceSet(entities.sourceSet)
  store.upsertSourceItem(entities.sourceItem)
  store.upsertDocument(entities.document)
  store.upsertVersion(entities.version)
  store.upsertRun(entities.run)
  store.upsertReview(entities.review)
  store.upsertReleaseTarget(entities.target)

  for (const entity of Object.values(entities)) entity.external.value = 'Mutated outside store'
  entities.version.blocks[0].content.text = 'Mutated block'
  entities.version.lineage[0].relation = 'merged'
  entities.review.steps[0].comment = 'Mutated review'
  entities.target.config_snapshot.mode.name = 'Mutated config'
  entities.sourceItem.metadata.label = 'Mutated metadata'
  entities.run.state_payload.stage.name = 'Mutated run'

  const stored = [
    store.projectsById['project-1'], store.sourceSetsById['source-set-1'], store.sourceItemsById['source-item-1'],
    store.documentsById['document-1'], store.versionsById['version-1'], store.runsById['run-1'],
    store.reviewsById['review-1'], store.releaseTargetsById['target-1'],
  ] as unknown as Array<{ external: { value: string } }>
  assert.deepEqual(stored.map(entity => entity.external.value), [
    'Original project', 'Original source set', 'Original source item', 'Original document',
    'Original version', 'Original run entity', 'Original review entity', 'Original target',
  ])
  assert.deepEqual(store.versionsById['version-1'].blocks[0].content, { text: 'Original block' })
  assert.equal(store.versionsById['version-1'].lineage?.[0].relation, 'same')
  assert.equal(store.reviewsById['review-1'].steps?.[0].comment, 'Original review')
  assert.deepEqual(store.releaseTargetsById['target-1'].config_snapshot, { mode: { name: 'Original config' } })
  assert.deepEqual(store.sourceItemsById['source-item-1'].metadata, { label: 'Original metadata' })
  assert.deepEqual(store.runsById['run-1'].state_payload, { stage: { name: 'Original run' } })
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
