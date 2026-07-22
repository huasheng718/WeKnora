import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import test from 'node:test'

const pageUrl = new URL('./ProductionDocumentTypeManagement.vue', import.meta.url)
const apiSource = readFileSync(new URL('../../api/production/index.ts', import.meta.url), 'utf8')
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

test('management page exposes built-in inspection and server-owned draft derivation', () => {
  const source = readFileSync(pageUrl, 'utf8')
  assert.match(apiSource, /export type ProductionDocumentTypeOrigin = ['"]builtin['"] \| ['"]custom['"]/)
  assert.match(apiSource, /origin:\s*ProductionDocumentTypeOrigin/)
  assert.match(apiSource, /template_key\?:\s*string \| null/)
  assert.match(apiSource, /export function deriveProductionDocumentType/)
  assert.match(apiSource, /`\/api\/v1\/production\/document-types\/\$\{id\}\/drafts`/)
  assert.match(apiSource, /command\.payload,\s*productionCommandConfig\(command\)/)

  assert.match(source, /type DocumentTypeDrawerMode = ['"]create['"] \| ['"]derive['"]/)
  assert.match(source, /documentTypeOriginBadge/)
  assert.match(source, /documentTypeConfigurationSummary/)
  assert.match(source, /openConfigurationDrawer/)
  assert.match(source, /openDeriveDrawer/)
  assert.match(source, /deriveProductionDocumentType/)
  assert.match(source, /canDeriveProductionDocumentType/)
  assert.match(source, /v-if="canDeriveProductionDocumentType\(currentRole, item\.status\)"/)
  assert.match(source, /production\.documentTypes\.generatedVersion/)
  assert.match(source, /production\.documentTypes\.fields\.templateKey/)
  assert.match(source, /class="raw-config-json"/)
  assert.match(source, /white-space:\s*pre-wrap/)
  assert.match(source, /deriveCommand\?\.signature === signature/)
  assert.match(source, /await loadDocumentTypes\(\)/)
})
