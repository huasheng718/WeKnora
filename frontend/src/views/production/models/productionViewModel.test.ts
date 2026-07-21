import assert from 'node:assert/strict'
import test from 'node:test'
import { computed, ref } from 'vue'

import {
  canEditProductionProject,
  productionDocumentLocation,
  productionProjectLocation,
  productionProjectNameRules,
  projectListViewState,
  projectSummaryFromResponse,
  validateProductionProjectName,
  workbenchViewState,
} from './productionViewModel'

test('authors can edit active projects while archived projects remain read-only', () => {
  assert.equal(canEditProductionProject('contributor', { status: 'active', current_user_roles: ['author'] }), true)
  assert.equal(canEditProductionProject('contributor', { status: 'archived', current_user_roles: ['author'] }), false)
  assert.equal(canEditProductionProject('viewer', { status: 'active', current_user_roles: ['project_owner'] }), false)
})

test('project list and workbench expose stable loading error empty and ready states', () => {
  assert.equal(projectListViewState({ loading: true, loaded: false, error: '', itemCount: 0 }), 'loading')
  assert.equal(projectListViewState({ loading: false, loaded: false, error: 'offline', itemCount: 0 }), 'error')
  assert.equal(projectListViewState({ loading: false, loaded: true, error: '', itemCount: 0 }), 'empty')
  assert.equal(projectListViewState({ loading: true, loaded: true, error: '', itemCount: 2 }), 'ready')
  assert.equal(workbenchViewState({ loading: true, loaded: false, error: '', hasProject: false }), 'loading')
  assert.equal(workbenchViewState({ loading: false, loaded: true, error: '', hasProject: false }), 'missing')
  assert.equal(workbenchViewState({ loading: false, loaded: true, error: '', hasProject: true }), 'ready')
})

test('missing project summaries degrade to local zeros without hiding the project', () => {
  const summary = projectSummaryFromResponse({ updated_at: '2026-07-20T10:00:00Z' })

  assert.deepEqual(summary, {
    documentCount: 0,
    sourceSetCount: 0,
    pendingReviews: 0,
    failedTargets: 0,
    inFlightRuns: 0,
    latestActivity: '2026-07-20T10:00:00Z',
  })
})

test('project dialog validation counts trimmed Unicode code points', () => {
  assert.equal(validateProductionProjectName('   '), 'required')
  assert.equal(validateProductionProjectName('😀'.repeat(255)), null)
  assert.equal(validateProductionProjectName('😀'.repeat(256)), 'too_long')
})

test('project dialog rules recompute translated messages after locale changes', () => {
  const locale = ref('en-US')
  const t = (key: string) => `${locale.value}:${key}`
  const rules = computed(() => productionProjectNameRules(t))

  assert.equal(rules.value.name[0].message, 'en-US:production.validation.nameRequired')
  locale.value = 'zh-CN'
  assert.equal(rules.value.name[0].message, 'zh-CN:production.validation.nameRequired')
  assert.equal(rules.value.name[1].message, 'zh-CN:production.validation.nameTooLong')
})

test('production navigation uses named project and document routes', () => {
  assert.deepEqual(productionProjectLocation('project-1'), { name: 'productionProject', params: { projectId: 'project-1' } })
  assert.deepEqual(productionDocumentLocation('document-1'), { name: 'productionDocument', params: { documentId: 'document-1' } })
})
