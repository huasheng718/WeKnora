import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionDocumentType } from '@/api/production'
import {
  DocumentTypeDerivationLifecycle,
  canDeriveProductionDocumentType,
  canManageProductionDocumentTypes,
  documentTypeControlVisibility,
  documentTypeConfigurationSummary,
  documentTypeListViewState,
  documentTypeOriginBadge,
  parseDerivedDocumentTypeDraft,
  parseDocumentTypeDraft,
  prefillDerivedDocumentTypeForm,
  productionRequestFailure,
  reconcileDocumentTypeDerivationBase,
  sortDocumentTypeVersions,
} from './documentTypeManagement'

function documentType(code: string, schemaVersion: number, updatedAt: string): ProductionDocumentType {
  return {
    id: `${code}-${schemaVersion}`,
    tenant_id: 7,
    code,
    name: code,
    description: '',
    schema_version: schemaVersion,
    block_schema: {},
    source_requirements: {},
    skill_bindings: {},
    workflow_plan: {},
    quality_rules: {},
    review_policy: {},
    publication_policy: {},
    status: 'draft',
    origin: 'custom',
    template_key: null,
    created_by: 'user-1',
    created_at: updatedAt,
    updated_at: updatedAt,
    deleted_at: null,
  }
}

test('only tenant admin and owner can manage document types', () => {
  assert.equal(canManageProductionDocumentTypes('owner'), true)
  assert.equal(canManageProductionDocumentTypes('admin'), true)
  assert.equal(canManageProductionDocumentTypes('contributor'), false)
  assert.equal(canManageProductionDocumentTypes('viewer'), false)
  assert.equal(canManageProductionDocumentTypes(''), false)
})

test('only tenant admin and owner can derive active or retired document types', () => {
  assert.equal(canDeriveProductionDocumentType('owner', 'active'), true)
  assert.equal(canDeriveProductionDocumentType('admin', 'retired'), true)
  assert.equal(canDeriveProductionDocumentType('admin', 'draft'), false)
  assert.equal(canDeriveProductionDocumentType('contributor', 'active'), false)
  assert.equal(canDeriveProductionDocumentType('viewer', 'retired'), false)
  assert.equal(canDeriveProductionDocumentType('admin', 'future' as never), false)
})

test('document type mutation controls are hidden outside their exact role and lifecycle states', () => {
  assert.deepEqual(documentTypeControlVisibility('viewer', 'active'), {
    inspect: true, create: false, activate: false, derive: false,
  })
  assert.deepEqual(documentTypeControlVisibility('contributor', 'draft'), {
    inspect: true, create: false, activate: false, derive: false,
  })
  assert.deepEqual(documentTypeControlVisibility('admin', 'draft'), {
    inspect: true, create: true, activate: true, derive: false,
  })
  assert.deepEqual(documentTypeControlVisibility('owner', 'active'), {
    inspect: true, create: true, activate: false, derive: true,
  })
  assert.deepEqual(documentTypeControlVisibility('owner', 'retired'), {
    inspect: true, create: true, activate: false, derive: true,
  })
  assert.deepEqual(documentTypeControlVisibility('owner', 'future' as never), {
    inspect: true, create: true, activate: false, derive: false,
  })
})

test('document type origin badge distinguishes governed built-ins from custom definitions', () => {
  assert.deepEqual(documentTypeOriginBadge('builtin'), {
    theme: 'primary',
    textKey: 'production.documentTypes.origin.builtin',
  })
  assert.deepEqual(documentTypeOriginBadge('custom'), {
    theme: 'default',
    textKey: 'production.documentTypes.origin.custom',
  })
})

test('document type configuration summary exposes readable governance facts', () => {
  const item = documentType('sop', 1, '2026-07-22T00:00:00Z')
  item.block_schema = { version: 1, required_sections: ['Scope', 'Steps'], allowed_block_types: ['heading'] }
  item.source_requirements = {
    version: 1,
    min_accepted_evidence: 2,
    allowed_source_kinds: ['upload', 'mcp'],
    require_evidence_section: true,
  }
  item.review_policy = { steps: ['business_reviewer', 'compliance_reviewer'] }
  item.publication_policy = {
    version: 1,
    target_type: 'knowledge_base',
    require_approved_review: true,
  }

  assert.deepEqual(documentTypeConfigurationSummary(item), {
    sections: ['Scope', 'Steps'],
    sourceKinds: ['upload', 'mcp'],
    minimumEvidence: 2,
    requiresEvidenceSection: true,
    reviewSteps: ['business_reviewer', 'compliance_reviewer'],
    publicationTarget: 'knowledge_base',
    requiresApprovedReview: true,
  })
})

test('document type list state makes load errors and empty data explicit', () => {
  assert.equal(documentTypeListViewState({ loading: true, loaded: false, error: '', itemCount: 0 }), 'loading')
  assert.equal(documentTypeListViewState({ loading: false, loaded: true, error: 'offline', itemCount: 0 }), 'error')
  assert.equal(documentTypeListViewState({ loading: false, loaded: true, error: '', itemCount: 0 }), 'empty')
  assert.equal(documentTypeListViewState({ loading: false, loaded: true, error: '', itemCount: 1 }), 'ready')
})

test('parseDocumentTypeDraft builds the complete API payload', () => {
  const result = parseDocumentTypeDraft({
    code: ' service_baseline ',
    name: ' Service baseline ',
    description: ' Governed delivery baseline ',
    schemaVersion: '2',
    blockSchema: '{"blocks":["summary"]}',
    sourceRequirements: '{}',
    skillBindings: '{"skills":["service-writer"]}',
    workflowPlan: '',
    qualityRules: '{}',
    reviewPolicy: '{}',
    publicationPolicy: '{}',
  })

  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.deepEqual(result.payload, {
    code: 'service_baseline',
    name: 'Service baseline',
    description: 'Governed delivery baseline',
    schema_version: 2,
    block_schema: { blocks: ['summary'] },
    source_requirements: {},
    skill_bindings: { skills: ['service-writer'] },
    workflow_plan: {},
    quality_rules: {},
    review_policy: {},
    publication_policy: {},
  })
})

test('parseDocumentTypeDraft reports the first invalid JSON field without producing a payload', () => {
  const result = parseDocumentTypeDraft({
    code: 'baseline', name: 'Baseline', description: '', schemaVersion: 1,
    blockSchema: '{broken', sourceRequirements: '{}', skillBindings: '{}', workflowPlan: '{}',
    qualityRules: '{}', reviewPolicy: '{}', publicationPolicy: '{}',
  })

  assert.deepEqual(result, { ok: false, reason: 'invalid_json', field: 'blockSchema' })
})

test('parseDocumentTypeDraft validates required identity and schema version', () => {
  const base = {
    code: 'baseline', name: 'Baseline', description: '', schemaVersion: 1,
    blockSchema: '{}', sourceRequirements: '{}', skillBindings: '{}', workflowPlan: '{}',
    qualityRules: '{}', reviewPolicy: '{}', publicationPolicy: '{}',
  }
  assert.deepEqual(parseDocumentTypeDraft({ ...base, code: ' ' }), { ok: false, reason: 'required', field: 'code' })
  assert.deepEqual(parseDocumentTypeDraft({ ...base, name: '' }), { ok: false, reason: 'required', field: 'name' })
  assert.deepEqual(parseDocumentTypeDraft({ ...base, schemaVersion: 0 }), { ok: false, reason: 'schema_version', field: 'schemaVersion' })
})

test('derived draft form prefills all editable JSON and omits lineage from the command payload', () => {
  const item = documentType('sop', 4, '2026-07-22T00:00:00Z')
  item.name = 'Standard operating procedure'
  item.description = 'Governed steps'
  item.origin = 'builtin'
  item.template_key = 'sop'
  item.block_schema = { required_sections: ['Scope'], version: 1 }
  item.source_requirements = { min_accepted_evidence: 1, version: 1 }
  item.skill_bindings = { skills: [], version: 1 }
  item.workflow_plan = { steps: [], version: 1 }
  item.quality_rules = { gates: ['section_completeness'], version: 1 }
  item.review_policy = { steps: ['business_reviewer'] }
  item.publication_policy = { target_type: 'knowledge_base', version: 1 }

  const form = prefillDerivedDocumentTypeForm(item)
  assert.equal(form.code, 'sop')
  assert.equal(form.name, 'Standard operating procedure')
  assert.equal(form.description, 'Governed steps')
  assert.equal(form.blockSchema, JSON.stringify(item.block_schema, null, 2))
  assert.equal(form.sourceRequirements, JSON.stringify(item.source_requirements, null, 2))
  assert.equal(form.skillBindings, JSON.stringify(item.skill_bindings, null, 2))
  assert.equal(form.workflowPlan, JSON.stringify(item.workflow_plan, null, 2))
  assert.equal(form.qualityRules, JSON.stringify(item.quality_rules, null, 2))
  assert.equal(form.reviewPolicy, JSON.stringify(item.review_policy, null, 2))
  assert.equal(form.publicationPolicy, JSON.stringify(item.publication_policy, null, 2))

  form.code = 'client-must-not-change-code'
  form.schemaVersion = 999
  const result = parseDerivedDocumentTypeDraft(form)
  assert.equal(result.ok, true)
  if (!result.ok) return
  assert.deepEqual(result.payload, {
    name: 'Standard operating procedure',
    description: 'Governed steps',
    block_schema: item.block_schema,
    source_requirements: item.source_requirements,
    skill_bindings: item.skill_bindings,
    workflow_plan: item.workflow_plan,
    quality_rules: item.quality_rules,
    review_policy: item.review_policy,
    publication_policy: item.publication_policy,
  })
  assert.equal('code' in result.payload, false)
  assert.equal('schema_version' in result.payload, false)
})

test('structured request failures retain server status and message', () => {
  assert.deepEqual(productionRequestFailure({ status: 409, message: 'version already allocated' }, 'fallback'), {
    status: 409,
    message: 'version already allocated',
  })
  assert.deepEqual(productionRequestFailure({ response: { status: 503, data: { error: { message: 'temporarily unavailable' } } } }, 'fallback'), {
    status: 503,
    message: 'temporarily unavailable',
  })
  assert.deepEqual(productionRequestFailure({ message: '   ' }, 'fallback'), { status: null, message: 'fallback' })
})

test('derivation lifecycle reuses unchanged commands and replaces changed payload or base commands', () => {
  const lifecycle = new DocumentTypeDerivationLifecycle<{ key: string }>()
  let created = 0
  const createCommand = () => ({ key: `command-${++created}` })
  const payload = { name: 'SOP', block_schema: { version: 1 } }

  const first = lifecycle.prepare('base-1', payload, createCommand)
  assert.equal(lifecycle.prepare('base-1', { ...payload }, createCommand), first)

  const changedPayload = lifecycle.prepare('base-1', { ...payload, name: 'Changed SOP' }, createCommand)
  assert.notEqual(changedPayload, first)

  const changedBase = lifecycle.prepare('base-2', { ...payload, name: 'Changed SOP' }, createCommand)
  assert.notEqual(changedBase, changedPayload)
  assert.equal(created, 3)
})

test('structured 409 preserves form and command while refreshing and reconciling the exact base', async () => {
  const lifecycle = new DocumentTypeDerivationLifecycle<{ key: string }>()
  const command = lifecycle.prepare('sop-1', { name: 'Edited SOP' }, () => ({ key: 'stable-command' }))
  const form = { name: 'Edited SOP', description: 'Unsaved input' }
  const base = documentType('sop', 1, '2026-07-22T00:00:00Z')
  base.id = 'sop-1'
  base.status = 'active'
  const refreshedBase = { ...base, name: 'Server-refreshed SOP', status: 'retired' as const }
  let refreshes = 0

  const result = await lifecycle.fail(
    { status: 409, message: 'a newer version exists' },
    'derive failed',
    form,
    base,
    async () => { refreshes += 1; return [refreshedBase] },
  )

  assert.equal(result.form, form)
  assert.equal(result.preserveInput, true)
  assert.equal(result.command, command)
  assert.equal(result.message, 'a newer version exists')
  assert.equal(result.status, 409)
  assert.equal(result.refreshed, true)
  assert.equal(result.base, refreshedBase)
  assert.equal(refreshes, 1)
  assert.equal(lifecycle.prepare('sop-1', { name: 'Edited SOP' }, () => ({ key: 'new-command' })), command)
})

test('non-conflict failure preserves input without refresh and success refreshes then clears command', async () => {
  const lifecycle = new DocumentTypeDerivationLifecycle<{ key: string }>()
  const command = lifecycle.prepare('base-1', { name: 'Draft' }, () => ({ key: 'command-1' }))
  const form = { name: 'Draft' }
  const base = documentType('sop', 1, '2026-07-22T00:00:00Z')
  let refreshes = 0
  const refresh = async () => { refreshes += 1; return [base] }

  const failure = await lifecycle.fail({ status: 503, message: 'service unavailable' }, 'fallback', form, base, refresh)
  assert.equal(failure.form, form)
  assert.equal(failure.command, command)
  assert.equal(failure.refreshed, false)
  assert.equal(refreshes, 0)

  const success = await lifecycle.succeed(refresh)
  assert.equal(success.refreshed, true)
  assert.deepEqual(success.items, [base])
  assert.equal(refreshes, 1)
  assert.equal(lifecycle.currentCommand, null)
})

test('base reconciliation fails closed for missing draft or malformed refreshed rows', () => {
  const base = documentType('sop', 1, '2026-07-22T00:00:00Z')
  base.id = 'base-1'
  base.status = 'active'
  const exact = { ...base, status: 'retired' as const }
  assert.equal(reconcileDocumentTypeDerivationBase(base, [exact]), exact)
  assert.equal(reconcileDocumentTypeDerivationBase(base, [{ ...exact, status: 'draft' }]), null)
  assert.equal(reconcileDocumentTypeDerivationBase(base, [{ ...exact, status: 'future' as never }]), null)
  assert.equal(reconcileDocumentTypeDerivationBase(base, []), null)
})

test('document type versions group by code and newest schema version first', () => {
  const sorted = sortDocumentTypeVersions([
    documentType('zeta', 1, '2026-07-20T00:00:00Z'),
    documentType('alpha', 1, '2026-07-22T00:00:00Z'),
    documentType('alpha', 3, '2026-07-19T00:00:00Z'),
    documentType('alpha', 2, '2026-07-21T00:00:00Z'),
  ])
  assert.deepEqual(sorted.map(item => item.id), ['alpha-3', 'alpha-2', 'alpha-1', 'zeta-1'])
})
