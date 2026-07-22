import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import test from 'node:test'
import { compile } from '@vue/compiler-dom'
import { parse } from '@vue/compiler-sfc'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from '@vue/server-renderer'

import { documentTypeControlVisibility } from './models/documentTypeManagement'

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
  assert.match(source, /createLatestRequestCoordinator\(\)/)
  assert.match(source, /onBeforeUnmount\(\(\) => \{[\s\S]*loadCoordinator\.invalidate\(\)/)
  assert.match(source, /class="table-scroll"/)
  assert.match(source, /const activationCommands = new Map/)
  assert.match(source, /activationCommands\.delete\(item\.id\)/)
})

interface ManagementRenderOptions {
  drawerVisible?: boolean
  drawerMode?: 'create' | 'derive'
  deriveBase?: Record<string, unknown> | null
  formError?: string
  deriveRequestFailed?: boolean
  viewState?: 'empty' | 'ready'
  documentTypes?: Array<Record<string, unknown>>
}

async function renderManagementTemplate(
  role: 'owner' | 'admin' | 'contributor' | 'viewer',
  status: 'draft' | 'active' | 'retired',
  options: ManagementRenderOptions = {},
) {
  const source = readFileSync(pageUrl, 'utf8')
  const descriptor = parse(source, { filename: 'ProductionDocumentTypeManagement.vue' }).descriptor
  assert.ok(descriptor.template)
  const { code } = compile(descriptor.template.content, { mode: 'function' })
  const render = Function('Vue', code)(await import('vue'))
  render._rc = true
  const pageControls = documentTypeControlVisibility(role)
  const item = {
    id: 'type-1', code: 'sop', name: 'SOP', description: '', schema_version: 1,
    status, origin: 'builtin', template_key: 'sop', updated_at: '2026-07-22T00:00:00Z',
  }
  const app = createSSRApp(defineComponent({
    render,
    setup: () => ({
      t: (key: string) => key,
      backToProjects: () => {}, loading: false, loadDocumentTypes: () => {},
      pageControls, openCreateDrawer: () => {},
      viewState: options.viewState ?? 'ready', skeletonRows: [], error: '',
      documentTypes: options.documentTypes ?? [item], activeCountLabel: '',
      documentTypeOriginBadge: () => ({ theme: 'primary', textKey: 'production.documentTypes.origin.builtin' }),
      statusTheme: () => 'default', formatDate: () => 'date', openConfigurationDrawer: () => {},
      rowControls: (rowStatus: typeof status) => documentTypeControlVisibility(role, rowStatus),
      submitting: false, openDeriveDrawer: () => {}, activatingId: '', confirmActivation: () => {},
      configurationDrawerVisible: false, inspectedDocumentType: null, inspectionSummary: null,
      summaryList: () => '', requirementLabel: () => '', rawConfigFields: [], rawConfiguration: () => '',
      drawerVisible: options.drawerVisible ?? false,
      drawerMode: options.drawerMode ?? 'create',
      formError: options.formError ?? '',
      deriveRequestFailed: options.deriveRequestFailed ?? false,
      deriveBase: options.deriveBase === undefined ? null : options.deriveBase,
      form: { code: 'sop', name: 'Preserved input', description: 'Unsaved reference' },
      jsonFields: [], submitDraft: () => {},
    }),
  }))
  const passthrough = defineComponent({ inheritAttrs: false, setup: (_, { slots, attrs }) => () => h('span', attrs, slots.default?.()) })
  app.component('t-button', defineComponent({ inheritAttrs: false, setup: (_, { slots, attrs }) => () => h('button', attrs, slots.default?.()) }))
  app.component('t-tooltip', passthrough)
  app.component('t-tag', passthrough)
  app.component('t-alert', passthrough)
  app.component('t-skeleton', passthrough)
  app.component('t-icon', passthrough)
  app.component('t-form', passthrough)
  app.component('t-form-item', passthrough)
  app.component('t-input', passthrough)
  app.component('t-input-number', passthrough)
  app.component('t-textarea', passthrough)
  app.component('t-drawer', defineComponent({
    inheritAttrs: false,
    props: { visible: Boolean },
    setup(props, { slots }) {
      return () => props.visible ? h('aside', slots.default?.()) : null
    },
  }))
  return renderToString(app)
}

test('compiled management template hides mutation controls from contributor and viewer roles', async () => {
  for (const role of ['contributor', 'viewer'] as const) {
    for (const status of ['draft', 'active', 'retired'] as const) {
      const html = await renderManagementTemplate(role, status)
      assert.match(html, /production\.documentTypes\.viewConfiguration/)
      assert.doesNotMatch(html, /production\.documentTypes\.create/)
      assert.doesNotMatch(html, /production\.documentTypes\.activate/)
      assert.doesNotMatch(html, /production\.documentTypes\.deriveDraft/)
    }
  }
})

test('compiled management template shows only lifecycle-valid admin and owner mutation controls', async () => {
  for (const role of ['admin', 'owner'] as const) {
    const draft = await renderManagementTemplate(role, 'draft')
    assert.match(draft, /production\.documentTypes\.create/)
    assert.match(draft, /production\.documentTypes\.activate/)
    assert.doesNotMatch(draft, /production\.documentTypes\.deriveDraft/)

    for (const status of ['active', 'retired'] as const) {
      const html = await renderManagementTemplate(role, status)
      assert.match(html, /production\.documentTypes\.create/)
      assert.match(html, /production\.documentTypes\.deriveDraft/)
      assert.doesNotMatch(html, /production\.documentTypes\.activate/)
    }
  }
})

test('compiled derive drawer preserves input but exposes no submit when refreshed base is unavailable', async () => {
  const html = await renderManagementTemplate('admin', 'active', {
    drawerVisible: true,
    drawerMode: 'derive',
    deriveBase: null,
    formError: 'server conflict',
    viewState: 'empty',
    documentTypes: [],
  })

  assert.match(html, /production\.documentTypes\.deriveBaseUnavailable/)
  assert.match(html, /Preserved input/)
  assert.match(html, /production\.actions\.cancel/)
  assert.doesNotMatch(html, /production\.documentTypes\.deriveDraft/)
})

test('compiled derive drawer shows command reuse hint only after a request failure', async () => {
  const base = {
    id: 'type-1', code: 'sop', name: 'SOP', description: '', schema_version: 1,
    status: 'active', origin: 'builtin', template_key: 'sop', updated_at: '2026-07-22T00:00:00Z',
  }
  const validationFailure = await renderManagementTemplate('admin', 'active', {
    drawerVisible: true,
    drawerMode: 'derive',
    deriveBase: base,
    formError: 'invalid JSON',
    deriveRequestFailed: false,
    viewState: 'empty',
    documentTypes: [],
  })
  assert.doesNotMatch(validationFailure, /production\.documentTypes\.deriveRetryHint/)

  const requestFailure = await renderManagementTemplate('admin', 'active', {
    drawerVisible: true,
    drawerMode: 'derive',
    deriveBase: base,
    formError: 'service unavailable',
    deriveRequestFailed: true,
    viewState: 'empty',
    documentTypes: [],
  })
  assert.match(requestFailure, /production\.documentTypes\.deriveRetryHint/)
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
  assert.match(source, /production\.documentTypes\.generatedVersion/)
  assert.match(source, /production\.documentTypes\.fields\.templateKey/)
  assert.match(source, /class="raw-config-json"/)
  assert.match(source, /white-space:\s*pre-wrap/)
})
