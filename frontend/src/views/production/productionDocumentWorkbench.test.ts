import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function source(path: string) {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}

test('document workbench coordinates real document evidence annotation and run reads', () => {
  const workbench = source('./ProductionDocumentWorkbench.vue')
  for (const api of [
    'getProductionDocument',
    'listProductionDocumentVersions',
    'getProductionDocumentVersion',
    'listProductionEvidence',
    'listProductionAnnotations',
    'listProductionDocumentRuns',
    'listProductionRunToolCalls',
  ]) assert.match(workbench, new RegExp(api))
  assert.match(workbench, /createLatestRequestCoordinator\(\)/)
  assert.match(workbench, /loadProductionAnnotationPages\(page => listProductionAnnotations/)
  assert.match(workbench, /page_size:\s*productionAnnotationPageSize/)
  assert.match(workbench, /onBeforeUnmount\(\(\) => loadCoordinator\.invalidate\(\)\)/)
  assert.doesNotMatch(workbench, /\bmock\b/i)
})

test('document workbench keeps a stable three-column editor and responsive side drawers', () => {
  const workbench = source('./ProductionDocumentWorkbench.vue')
  assert.match(workbench, /grid-template-columns:\s*minmax\(220px,\s*280px\)\s+minmax\(520px,\s*1fr\)\s+minmax\(280px,\s*360px\)/)
  assert.match(workbench, /@media\s*\(max-width:\s*1099px\)/)
  assert.match(workbench, /<t-drawer[\s\S]*placement="left"/)
  assert.match(workbench, /<t-drawer[\s\S]*placement="right"/)
  assert.match(workbench, /min-width:\s*320px/)
  assert.match(workbench, /<t-tooltip/g)
})

test('document workbench composes every focused production tool panel', () => {
  const workbench = source('./ProductionDocumentWorkbench.vue')
  for (const component of [
    'ProductionOutline',
    'ProductionEvidencePanel',
    'ProductionBlockEditor',
    'ProductionAnnotationPanel',
    'ProductionVersionPanel',
    'ProductionAIRunPanel',
  ]) assert.match(workbench, new RegExp(`<${component}`))
  assert.match(source('./components/ProductionBlockEditor.vue'), /<ProductionBlockToolbar/)
})

test('production document route renders the real workbench', () => {
  const router = source('../../router/index.ts')
  assert.match(router, /name:\s*"productionDocument"[\s\S]*ProductionDocumentWorkbench\.vue/)
})

test('icon-only controls are labelled and AI runs are keyboard buttons', () => {
  const workbench = source('./ProductionDocumentWorkbench.vue')
  const toolbar = source('./components/ProductionBlockToolbar.vue')
  const versions = source('./components/ProductionVersionPanel.vue')
  const runs = source('./components/ProductionAIRunPanel.vue')
  assert.doesNotMatch(workbench, /shape="square"(?![^>]*aria-label)/)
  assert.doesNotMatch(toolbar, /shape="square"(?![^>]*aria-label)/)
  assert.doesNotMatch(versions, /shape="square"(?![^>]*aria-label)/)
  assert.match(runs, /<button[^>]*class="run-row"/)
  assert.doesNotMatch(runs, /\{\{\s*(?:run\.status|call\.approval_status)\s*\}\}/)
})

test('annotation mutations are scoped to the current document lifecycle', () => {
  const workbench = source('./ProductionDocumentWorkbench.vue')
  assert.match(workbench, /createScopedMutationCoordinator\(\)/)
  assert.match(workbench, /annotationMutationCoordinator\.start\(requestedDocumentId\)/)
  assert.match(workbench, /annotationMutationCoordinator\.isCurrent\(mutation,\s*documentId\.value\)/)
  assert.match(workbench, /watch\(documentId,[\s\S]*annotationMutationCoordinator\.invalidate\(\)/)
  assert.match(workbench, /watch\(documentId,[\s\S]*annotationSubmitting\.value = false/)
  assert.match(workbench, /onBeforeUnmount\(\(\) => annotationMutationCoordinator\.invalidate\(\)\)/)
})
