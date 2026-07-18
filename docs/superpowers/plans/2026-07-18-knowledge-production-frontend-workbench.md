# Knowledge Production Frontend Workbench Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the first-level Knowledge Production navigation and a complete project, document, evidence, annotation, review and publication workflow inside the existing WeKnora platform shell.

**Architecture:** A production API module and Pinia store provide typed state. Route-level views remain thin and delegate evidence, document, review and publication state to focused components and pure model helpers that can be tested with the repository's Node test runner.

**Tech Stack:** Vue 3, TypeScript 6, Pinia 3, Vue Router 4, TDesign Vue Next, existing request utility, `tsx --test`, vue-tsc, Vite, Playwright QA.

## Global Constraints

- Requires Publication Projection plan.
- Add “知识生产” immediately after “知识库” in the existing sidebar.
- Reuse `Platform` shell and TDesign tokens; do not create a second application shell.
- Keep sections unframed; use cards only for repeated project/document items and modals.
- Document workbench layout is outline/evidence left, block editor center, AI/annotation/version right.
- Never expose publish actions until target preflight succeeds and the user has project role `publisher`.
- Support zh-CN, en-US, ko-KR and ru-RU keys in the same change.
- Every fixed toolbar, side panel and block action has stable responsive dimensions.
- The production API client sends one stable `Idempotency-Key` per user command and reuses it only for retries of the same payload.

---

### Task 1: Add Typed Production API and Store

**Files:**
- Create: `frontend/src/api/production/index.ts`
- Create: `frontend/src/api/production/idempotency.ts`
- Create: `frontend/src/api/production/idempotency.test.ts`
- Modify: `frontend/src/utils/request.ts`
- Create: `frontend/src/stores/production.ts`
- Create: `frontend/src/views/production/models/productionState.ts`
- Create: `frontend/src/views/production/models/productionState.test.ts`

**Interfaces:**
- Consumes: `get`, `post`, `put`, `del` from `frontend/src/utils/request.ts`.
- Produces: typed project, source, document, review, run and release clients plus store loading/error state.

- [ ] **Step 1: Write failing normalization tests**

```ts
test('groups release targets by actionable state', () => {
  const result = groupReleaseTargets([
    { id: 'a', status: 'active' },
    { id: 'b', status: 'failed' },
    { id: 'c', status: 'building' },
  ] as ProductionReleaseTarget[])
  assert.deepEqual(result.retryable.map(i => i.id), ['b'])
  assert.deepEqual(result.inFlight.map(i => i.id), ['c'])
})

test('a production command keeps its idempotency key across retries', () => {
  const command = createProductionCommand({ name: 'Baseline' }, () => 'request-1')
  assert.equal(command.idempotencyKey, 'request-1')
  assert.equal(productionCommandConfig(command).headers['Idempotency-Key'], 'request-1')
  assert.equal(productionCommandConfig(command).headers['Idempotency-Key'], 'request-1')
})
```

- [ ] **Step 2: Run and verify failure**

Run: `cd frontend && npx tsx --test src/api/production/idempotency.test.ts src/views/production/models/productionState.test.ts`
Expected: FAIL because module is missing.

- [ ] **Step 3: Add API contracts and normalized store**

```ts
export interface ProductionDocumentVersion {
  id: string
  document_id: string
  version_number: number
  source_set_id: string
  origin: 'ai' | 'human' | 'mixed' | 'rollback'
  blocks: ProductionDocumentBlock[]
  frozen_at?: string
}

export function getProductionDocument(id: string) {
  return get(`/api/v1/production/documents/${id}`)
}

export function decideProductionToolCall(id: string, command: ProductionCommand<{ decision: 'approve' | 'reject'; reason: string }>) {
  return post(`/api/v1/production/tool-calls/${id}/decision`, command.payload, productionCommandConfig(command))
}

export function appendProductionVersion(documentId: string, currentVersionId: string, command: ProductionCommand<AppendProductionVersionInput>) {
  const config = productionCommandConfig(command)
  config.headers['If-Match'] = currentVersionId
  return post(`/api/v1/production/documents/${documentId}/versions`, command.payload, config)
}
```

`createProductionCommand` returns `{ idempotencyKey, payload }`; retry handlers retain that object instead of generating a new key. `productionCommandConfig` maps it to the `Idempotency-Key` header. Extend `del` in `request.ts` to accept an optional Axios config so POST, PUT and DELETE production commands share the same helper. Store collections by ID and keep `activeProjectId`, `activeDocumentId`, `activeVersionId`, loading keys and last error separately.

- [ ] **Step 4: Run tests and type check**

Run: `cd frontend && npx tsx --test src/api/production/idempotency.test.ts src/views/production/models/productionState.test.ts && npm run type-check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/api/production frontend/src/utils/request.ts frontend/src/stores/production.ts frontend/src/views/production/models/productionState*
git commit -m "feat(production-ui): add typed production state"
```

### Task 2: Add Route, Sidebar Entry and Permission Visibility

**Files:**
- Create: `frontend/src/views/production/models/productionAccess.ts`
- Create: `frontend/src/views/production/models/productionAccess.test.ts`
- Modify: `frontend/src/router/index.ts`
- Modify: `frontend/src/stores/menu.ts`
- Modify: `frontend/src/components/menu.vue`
- Modify: `frontend/src/i18n/locales/zh-CN.ts`
- Modify: `frontend/src/i18n/locales/en-US.ts`
- Modify: `frontend/src/i18n/locales/ko-KR.ts`
- Modify: `frontend/src/i18n/locales/ru-RU.ts`

**Interfaces:**
- Consumes: auth store TenantRole and project roles returned by API.
- Produces: `canViewProduction`, `canEditProduction`, `canPublishProduction` and three route entries.

- [ ] **Step 1: Write failing access tests**

```ts
test('viewer can view but cannot author or publish', () => {
  const access = productionAccess('viewer', ['observer'])
  assert.equal(access.view, true)
  assert.equal(access.edit, false)
  assert.equal(access.publish, false)
})

test('publisher role requires contributor tenant floor', () => {
  assert.equal(productionAccess('viewer', ['publisher']).publish, false)
  assert.equal(productionAccess('contributor', ['publisher']).publish, true)
})
```

- [ ] **Step 2: Run and verify failure**

Run: `cd frontend && npx tsx --test src/views/production/models/productionAccess.test.ts`
Expected: FAIL.

- [ ] **Step 3: Add menu item and routes**

```ts
interface MenuItem {
  title: string
  titleKey?: string
  iconType: 'image' | 'tdesign'
  icon: string
  path: string
  childrenPath?: string
  children?: MenuChild[]
}

// Keep every existing menu row on iconType: 'image' with its current icon value.
{ title: '', titleKey: 'menu.knowledgeBase', iconType: 'image', icon: 'zhishiku', path: 'knowledge-bases' },
{ title: '', titleKey: 'menu.knowledgeProduction', iconType: 'tdesign', icon: 'file-copy', path: 'knowledge-production' },
```

Render `iconType: 'tdesign'` with `<t-icon :name="item.icon" />`; retain the existing `getImgSrc(...)` branch unchanged for `iconType: 'image'`. This places the TDesign `file-copy` icon immediately after Knowledge Base without adding an SVG asset.

Register:

```ts
{
  path: 'knowledge-production',
  name: 'productionProjects',
  component: () => import('../views/production/ProductionProjectList.vue'),
},
{
  path: 'knowledge-production/projects/:projectId',
  name: 'productionProject',
  component: () => import('../views/production/ProductionProjectWorkbench.vue'),
},
{
  path: 'knowledge-production/documents/:documentId',
  name: 'productionDocument',
  component: () => import('../views/production/ProductionDocumentWorkbench.vue'),
},
```

Add translated labels in all four locale files. Use an existing TDesign icon mapping instead of a custom SVG.

- [ ] **Step 4: Run access, terminology and type tests**

Run: `cd frontend && npm test && npm run type-check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/router/index.ts frontend/src/stores/menu.ts frontend/src/components/menu.vue frontend/src/i18n/locales/*.ts frontend/src/views/production/models/productionAccess*
git commit -m "feat(production-ui): add knowledge production navigation"
```

### Task 3: Build Project List and Project Workbench

**Files:**
- Create: `frontend/src/views/production/ProductionProjectList.vue`
- Create: `frontend/src/views/production/ProductionProjectWorkbench.vue`
- Create: `frontend/src/views/production/components/ProductionProjectDialog.vue`
- Create: `frontend/src/views/production/components/ProductionSourcePanel.vue`
- Create: `frontend/src/views/production/components/ProductionDocumentList.vue`
- Create: `frontend/src/views/production/models/projectSummary.ts`
- Create: `frontend/src/views/production/models/projectSummary.test.ts`

**Interfaces:**
- Consumes: project, source-set and document APIs/store.
- Produces: project create/list, tabs for sources/documents/reviews/releases and document navigation.

- [ ] **Step 1: Write failing summary-model tests**

```ts
test('project summary counts only actionable review and release rows', () => {
  const summary = summarizeProject(fixtureProject())
  assert.equal(summary.pendingReviews, 2)
  assert.equal(summary.failedTargets, 1)
  assert.equal(summary.inFlightRuns, 1)
})
```

- [ ] **Step 2: Run and verify failure**

Run: `cd frontend && npx tsx --test src/views/production/models/projectSummary.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement views with stable empty/loading/error states**

Project list uses a restrained responsive grid of individual project cards. Workbench uses top tabs, not nested cards:

```vue
<t-tabs v-model="activeTab" class="production-project-tabs">
  <t-tab-panel value="sources" :label="t('production.tabs.sources')" />
  <t-tab-panel value="documents" :label="t('production.tabs.documents')" />
  <t-tab-panel value="reviews" :label="t('production.tabs.reviews')" />
  <t-tab-panel value="releases" :label="t('production.tabs.releases')" />
</t-tabs>
```

- [ ] **Step 4: Run tests, type check and build**

Run: `cd frontend && npm test && npm run type-check && npm run build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/production/ProductionProject* frontend/src/views/production/components/ProductionProjectDialog.vue frontend/src/views/production/components/ProductionSourcePanel.vue frontend/src/views/production/components/ProductionDocumentList.vue frontend/src/views/production/models/projectSummary*
git commit -m "feat(production-ui): add project workbench"
```

### Task 4: Build Structured Document Workbench

**Files:**
- Create: `frontend/src/views/production/ProductionDocumentWorkbench.vue`
- Create: `frontend/src/views/production/components/ProductionOutline.vue`
- Create: `frontend/src/views/production/components/ProductionEvidencePanel.vue`
- Create: `frontend/src/views/production/components/ProductionBlockEditor.vue`
- Create: `frontend/src/views/production/components/ProductionBlockToolbar.vue`
- Create: `frontend/src/views/production/components/ProductionAnnotationPanel.vue`
- Create: `frontend/src/views/production/components/ProductionVersionPanel.vue`
- Create: `frontend/src/views/production/components/ProductionAIRunPanel.vue`
- Create: `frontend/src/views/production/models/documentBlocks.ts`
- Create: `frontend/src/views/production/models/documentBlocks.test.ts`

**Interfaces:**
- Consumes: immutable versions, append-version API, evidence, annotations and AI runs.
- Produces: block edit draft, evidence linking, annotation anchors, version diff and AI run controls.

- [ ] **Step 1: Write failing stable-block tests**

```ts
test('unchanged blocks retain logical ids in the next version payload', () => {
  const next = buildVersionPayload(versionFixture(), [{ op: 'edit', logicalBlockId: 'block-a', content: { text: 'new' } }])
  assert.equal(next.blocks[0].logical_block_id, 'block-a')
})

test('new blocks receive ids but never mutate the source version', () => {
  const source = versionFixture()
  const next = buildVersionPayload(source, [{ op: 'insertAfter', after: 'block-a', blockType: 'paragraph' }])
  assert.equal(source.blocks.length, 1)
  assert.equal(next.blocks.length, 2)
  assert.notEqual(next.blocks[1].logical_block_id, '')
})
```

- [ ] **Step 2: Run and verify failure**

Run: `cd frontend && npx tsx --test src/views/production/models/documentBlocks.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement three-column workbench**

Use CSS grid with stable constraints:

```less
.production-document-layout {
  display: grid;
  grid-template-columns: minmax(220px, 280px) minmax(520px, 1fr) minmax(280px, 360px);
  min-height: 0;
  height: 100%;
}
```

At widths below 1100px, move left/right panels into TDesign drawers opened by icon buttons with tooltips. Never shrink the editor below 320px.

- [ ] **Step 4: Run tests, type check and build**

Run: `cd frontend && npm test && npm run type-check && npm run build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/production/ProductionDocumentWorkbench.vue frontend/src/views/production/components/Production* frontend/src/views/production/models/documentBlocks*
git commit -m "feat(production-ui): add structured document workbench"
```

### Task 5: Add Review and Publication Flows

**Files:**
- Create: `frontend/src/views/production/components/ProductionReviewPanel.vue`
- Create: `frontend/src/views/production/components/ProductionVersionDiff.vue`
- Create: `frontend/src/views/production/components/ProductionReleaseDialog.vue`
- Create: `frontend/src/views/production/components/ProductionReleaseStatus.vue`
- Create: `frontend/src/views/production/models/reviewGate.ts`
- Create: `frontend/src/views/production/models/reviewGate.test.ts`
- Create: `frontend/src/views/production/models/releaseActions.ts`
- Create: `frontend/src/views/production/models/releaseActions.test.ts`

**Interfaces:**
- Consumes: review decision, release create/activate/retry/rollback APIs and production access helper.
- Produces: role-aware review buttons and per-target publication actions.

- [ ] **Step 1: Write failing gate/action tests**

```ts
test('blocking annotations disable submit with explicit reason', () => {
  assert.deepEqual(reviewSubmitGate({ openBlocking: 2, frozen: false }), {
    allowed: false,
    reason: 'blocking_annotations',
  })
})

test('failed target exposes retry but not activate', () => {
  assert.deepEqual(releaseActions({ status: 'failed' } as ProductionReleaseTarget), ['retry'])
})
```

- [ ] **Step 2: Run and verify failure**

Run: `cd frontend && npx tsx --test src/views/production/models/reviewGate.test.ts src/views/production/models/releaseActions.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement explicit review and release states**

Review panel displays frozen version, required role, reviewer, decision and comments. Release dialog requires target selection, confirmation, a rendered Markdown publication snapshot, and a read-only preflight preview containing chunking method, chunk size, overlap, separators, embedding model, graph enabled state, graph model and graph extraction options for each target KB. Target rows show `building`, `ready`, `active`, `failed` and `rolled_back` without collapsing partial failures into one generic state.

- [ ] **Step 4: Run frontend verification**

Run: `cd frontend && npm test && npm run type-check && npm run build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/production/components/ProductionReviewPanel.vue frontend/src/views/production/components/ProductionVersionDiff.vue frontend/src/views/production/components/ProductionRelease* frontend/src/views/production/models/reviewGate* frontend/src/views/production/models/releaseActions*
git commit -m "feat(production-ui): add review and publication workflows"
```

### Task 6: Perform Browser QA and Regression Verification

**Files:**
- Modify: `frontend/package.json`
- Modify: `frontend/package-lock.json`
- Create: `frontend/playwright.config.ts`
- Create: `frontend/e2e/fixtures/production-api.ts`
- Create: `frontend/e2e/production-workbench.spec.ts`

**Interfaces:**
- Consumes: the frontend dev server and deterministic production API route fixture.
- Produces: repeatable end-to-end workflow coverage and desktop/mobile screenshots.

- [ ] **Step 1: Pin Playwright and add the browser configuration**

Run:

```bash
cd frontend
npm install --save-dev --save-exact @playwright/test@1.61.1
npx playwright install chromium
```

Expected: `package.json` and `package-lock.json` contain exact version `1.61.1`, and Chromium installs successfully.

Create `frontend/playwright.config.ts`:

```ts
import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  retries: 1,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:5173',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  webServer: {
    command: 'npm run dev -- --host 127.0.0.1',
    url: 'http://127.0.0.1:5173',
    reuseExistingServer: true,
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
```

- [ ] **Step 2: Add the failing Playwright workflow**

```ts
import { expect, test } from '@playwright/test'

test('author to publisher workflow', async ({ page }) => {
  await page.goto('/platform/knowledge-production')
  await page.getByRole('button', { name: '新建项目' }).click()
  await page.getByLabel('项目名称').fill('AI 企微复盘')
  await page.getByRole('button', { name: '创建' }).click()
  await expect(page.getByText('AI 企微复盘')).toBeVisible()
})
```

- [ ] **Step 3: Run and verify the new test fails before fixture/UI completion**

Run: `cd frontend && npx playwright test e2e/production-workbench.spec.ts --project=chromium`
Expected: FAIL at the first unavailable production fixture or control.

- [ ] **Step 4: Implement the deterministic workflow fixture and critical-state assertions**

Create a route fixture with explicit state transitions:

```ts
export interface ProductionFixtureState {
  sourceStatus: 'draft' | 'frozen'
  runStatus: 'waiting_approval' | 'completed'
  blockingAnnotations: number
  reviewStatus: 'draft' | 'approved'
  targets: Record<'kb-primary' | 'kb-secondary', 'ready' | 'active' | 'failed' | 'rolled_back'>
}

export async function installProductionApiFixture(page: Page): Promise<ProductionFixtureState> {
  const state: ProductionFixtureState = {
    sourceStatus: 'draft',
    runStatus: 'waiting_approval',
    blockingAnnotations: 1,
    reviewStatus: 'draft',
    targets: { 'kb-primary': 'ready', 'kb-secondary': 'failed' },
  }
  await page.route('**/api/v1/production/**', route => dispatchProductionFixture(route, state))
  return state
}

async function dispatchProductionFixture(route: Route, state: ProductionFixtureState) {
  const request = route.request()
  const path = new URL(request.url()).pathname
  const command = `${request.method()} ${path}`
  if (command.endsWith('/freeze')) state.sourceStatus = 'frozen'
  if (command.includes('/tool-calls/') && command.endsWith('/decision')) state.runStatus = 'completed'
  if (command.includes('/annotations/') && command.endsWith('/status')) state.blockingAnnotations = 0
  if (command.includes('/reviews/') && command.endsWith('/decision')) state.reviewStatus = 'approved'
  if (command.endsWith('/release-targets/kb-primary/activate')) state.targets['kb-primary'] = 'active'
  if (command.endsWith('/release-targets/kb-secondary/retry')) {
    state.targets['kb-secondary'] = state.targets['kb-secondary'] === 'failed' ? 'ready' : 'active'
  }
  if (command.endsWith('/release-targets/kb-primary/rollback')) state.targets['kb-primary'] = 'rolled_back'
  await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, data: state }) })
}
```

Extend the fixture response for list/get routes with the same state fields and fixed project/document IDs used by the test. The test asserts each resulting badge/button, verifies the release dialog's chunk and graph preview, records browser console errors, and asserts `document.documentElement.scrollWidth <= window.innerWidth` after each viewport change. Capture `production-desktop.png` at 1440x900 and `production-mobile.png` at 390x844.

- [ ] **Step 5: Run complete frontend and browser suite**

```bash
cd frontend && npm test
cd frontend && npm run type-check
cd frontend && npm run build
cd frontend && npx playwright test e2e/production-workbench.spec.ts --project=chromium
```

Expected: PASS with no console errors, clipped text or overlapping controls.

- [ ] **Step 6: Commit**

```bash
git add frontend/package.json frontend/package-lock.json frontend/playwright.config.ts frontend/e2e/fixtures/production-api.ts frontend/e2e/production-workbench.spec.ts frontend/src/views/production frontend/src/i18n/locales
git commit -m "test(production-ui): cover governed document workflow"
```

## Plan Verification

Run:

```bash
cd frontend && npm test
cd frontend && npm run type-check
cd frontend && npm run build
cd frontend && npx playwright test e2e/production-workbench.spec.ts --project=chromium
```

Expected: every command passes and the existing Knowledge Base, Agent, Settings and chat routes remain visually and functionally unchanged.
