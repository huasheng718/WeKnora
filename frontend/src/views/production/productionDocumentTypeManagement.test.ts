import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import test from 'node:test'

const pageUrl = new URL('./ProductionDocumentTypeManagement.vue', import.meta.url)
const routerSource = readFileSync(new URL('../../router/index.ts', import.meta.url), 'utf8')
const projectListSource = readFileSync(new URL('./ProductionProjectList.vue', import.meta.url), 'utf8')

test('router and project list expose document type management', () => {
  assert.match(routerSource, /name:\s*["']productionDocumentTypes["']/)
  assert.match(routerSource, /path:\s*["']knowledge-production\/document-types["']/)
  assert.match(projectListSource, /productionDocumentTypes/)
  assert.match(projectListSource, /production\.documentTypes\.manage/)
})

test('management page coordinates requests, enforces role-aware controls, and cleans up loads', () => {
  assert.equal(existsSync(pageUrl), true, 'ProductionDocumentTypeManagement.vue must exist')
  if (!existsSync(pageUrl)) return

  const source = readFileSync(pageUrl, 'utf8')
  assert.match(source, /listProductionDocumentTypes/)
  assert.match(source, /createProductionDocumentType/)
  assert.match(source, /activateProductionDocumentType/)
  assert.match(source, /canManageProductionDocumentTypes/)
  assert.match(source, /createLatestRequestCoordinator\(\)/)
  assert.match(source, /onBeforeUnmount\(\(\) => \{[\s\S]*loadCoordinator\.invalidate\(\)/)
  assert.match(source, /:disabled="!canManage/)
  assert.match(source, /class="table-scroll"/)
  assert.match(source, /draftCommand\?\.signature === signature/)
  assert.match(source, /const activationCommands = new Map/)
  assert.match(source, /activationCommands\.delete\(item\.id\)/)
})
