import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionDocumentType } from '@/api/production'
import {
  canManageProductionDocumentTypes,
  documentTypeListViewState,
  parseDocumentTypeDraft,
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

test('document type versions group by code and newest schema version first', () => {
  const sorted = sortDocumentTypeVersions([
    documentType('zeta', 1, '2026-07-20T00:00:00Z'),
    documentType('alpha', 1, '2026-07-22T00:00:00Z'),
    documentType('alpha', 3, '2026-07-19T00:00:00Z'),
    documentType('alpha', 2, '2026-07-21T00:00:00Z'),
  ])
  assert.deepEqual(sorted.map(item => item.id), ['alpha-3', 'alpha-2', 'alpha-1', 'zeta-1'])
})
