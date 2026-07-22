import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

function source(path: string) { return readFileSync(new URL(path, import.meta.url), 'utf8') }

test('review panel exposes role-aware governed commands with lifecycle coordination', () => {
  const panel = source('./components/ProductionReviewPanel.vue')
  for (const contract of ['reviewSubmitGate', 'reviewStepActions', 'reviewTerminalActions', 'createScopedMutationCoordinator', 'createLatestRequestCoordinator', 'commandForReviewVersion']) {
    assert.match(panel, new RegExp(contract))
  }
  assert.match(panel, /onBeforeUnmount\(\(\) => \{ mutationCoordinator\.invalidate\(\); loadCoordinator\.invalidate\(\) \}\)/)
  assert.match(panel, /listProductionDocumentReviews/)
  assert.match(panel, /props\.versionId/)
})

test('review lifecycle settles submit state on version invalidation and localizes empty load failures', () => {
  const panel = source('./components/ProductionReviewPanel.vue')

  assert.match(panel, /watch\(\(\) => props\.versionId, \(\) => \{ mutationCoordinator\.invalidate\(\); submitting\.value = false; submitCommand = null \}\)/)
  assert.match(panel, /throw new Error\(response\.message \|\| t\('production\.reviewConsole\.loadFailed'\)\)/)
})

test('release dialog uses paged authoritative preflight and stable create commands', () => {
  const dialog = source('./components/ProductionReleaseDialog.vue')
  for (const contract of ['getProductionReleasePreflight', 'createProductionRelease', 'canConfirmProductionRelease', 'createScopedMutationCoordinator', 'createLatestRequestCoordinator']) {
    assert.match(dialog, new RegExp(contract))
  }
  assert.match(dialog, /has_more/)
  assert.match(dialog, /selectedKnowledgeBaseIds/)
  assert.match(dialog, /rendered_markdown/)
})

test('release preflight invalidates asynchronous state when its dialog scope changes', () => {
  const dialog = source('./components/ProductionReleaseDialog.vue')

  assert.match(dialog, /const preflightCoordinator = createLatestRequestCoordinator\(\)/)
  assert.match(dialog, /await preflightCoordinator\.run\(/)
  assert.match(dialog, /preflightCoordinator\.invalidate\(\)/)
  assert.match(dialog, /onBeforeUnmount\(\(\) => \{ mutationCoordinator\.invalidate\(\); preflightCoordinator\.invalidate\(\) \}\)/)
})

test('release status uses lifecycle actions, head locks, and rollback confirmation', () => {
  const status = source('./components/ProductionReleaseStatus.vue')
  for (const contract of ['releaseActions', 'releaseHistoryPage', 'appendReleaseHistory', 'createLatestRequestCoordinator', 'activateProductionReleaseTarget', 'retryProductionReleaseTarget', 'rollbackProductionReleaseTarget', 'head_lock_version']) {
    assert.match(status, new RegExp(contract))
  }
  assert.match(status, /DialogPlugin\.confirm/)
  assert.match(status, /if \(!props\.canPublish\) return/)
})

test('workbenches compose review diff and real publication surfaces', () => {
  const document = source('./ProductionDocumentWorkbench.vue')
  const project = source('./ProductionProjectWorkbench.vue')
  assert.match(document, /<ProductionReviewPanel/)
  assert.match(document, /<ProductionVersionDiff/)
  assert.match(project, /<ProductionReleaseStatus/)
  assert.match(project, /<ProductionReleaseDialog/)
  assert.doesNotMatch(project, /releases\.unavailableTitle/)
})

test('all locales expose review and publication console trees', () => {
  for (const locale of ['en-US', 'zh-CN', 'ko-KR', 'ru-RU']) {
    const text = source(`../../i18n/locales/${locale}.ts`)
    assert.match(text, /reviewConsole:/)
    assert.match(text, /releaseConsole:/)
  }
})
