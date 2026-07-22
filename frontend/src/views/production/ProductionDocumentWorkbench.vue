<template>
  <main class="document-workbench-page">
    <header class="document-topbar">
      <div class="document-identity">
        <t-tooltip :content="t('production.actions.backToProjects')">
          <t-button shape="square" variant="text" :aria-label="t('production.actions.backToProjects')" @click="backToProject"><t-icon name="chevron-left" /></t-button>
        </t-tooltip>
        <span class="document-kicker">{{ project?.name || t('production.workspace') }}</span>
        <span class="topbar-rule" />
        <h1>{{ document?.title || t('production.documentWorkbench.untitled') }}</h1>
        <t-tag v-if="document" size="small" variant="light" :theme="document.status === 'archived' ? 'default' : 'primary'">{{ t(`production.documentStatus.${document.status}`) }}</t-tag>
      </div>
      <div class="document-actions">
        <div class="responsive-tools">
          <t-tooltip :content="t('production.documentWorkbench.openContext')"><t-button shape="square" variant="outline" @click="leftDrawerVisible = true"><t-icon name="view-list" /></t-button></t-tooltip>
          <t-tooltip :content="t('production.documentWorkbench.openTools')"><t-button shape="square" variant="outline" @click="rightDrawerVisible = true"><t-icon name="control-platform" /></t-button></t-tooltip>
        </div>
        <t-input v-model="changeSummary" class="change-summary" size="small" :disabled="!canEdit" :placeholder="t('production.documentWorkbench.changeSummary')" />
        <t-tooltip :content="saveHint"><span><t-button size="small" :loading="saving" :disabled="!canSave" @click="saveVersion"><template #icon><t-icon name="save" /></template>{{ t('production.documentWorkbench.saveVersion') }}</t-button></span></t-tooltip>
      </div>
    </header>

    <div v-if="loading && !loaded" class="workbench-state"><t-loading size="medium" /> <span>{{ t('production.documentWorkbench.loading') }}</span></div>
    <div v-else-if="pageError && !document" class="workbench-state workbench-state--error" role="alert"><t-icon name="error-circle" /><span>{{ pageError }}</span><t-button size="small" variant="outline" @click="loadWorkbench()">{{ t('production.actions.retry') }}</t-button></div>
    <div v-else-if="!document" class="workbench-state"><t-icon name="file-unknown" /><span>{{ t('production.documentWorkbench.notFound') }}</span></div>

    <template v-else>
      <t-alert v-if="pageError" class="workbench-alert" theme="error" :message="pageError" />
      <t-alert v-if="conflict" class="workbench-alert" theme="warning" :message="t('production.documentWorkbench.conflict')">
        <template #operation><t-button size="small" variant="text" @click="reloadLatest">{{ t('production.documentWorkbench.reloadLatest') }}</t-button></template>
      </t-alert>
      <t-alert v-else-if="!canEdit" class="workbench-alert" theme="info" :message="readOnlyMessage" />

      <div class="production-document-layout">
        <aside class="left-rail">
          <ProductionOutline :blocks="draftBlocks" :active-logical-block-id="activeLogicalBlockId" @select="activeLogicalBlockId = $event" />
          <ProductionEvidencePanel :evidence="evidence" :linked-ids="activeBlock?.evidence_refs ?? []" :active-logical-block-id="activeLogicalBlockId" :can-edit="canEdit" @toggle="toggleEvidence" />
        </aside>

        <ProductionBlockEditor
          :blocks="draftBlocks"
          :active-logical-block-id="activeLogicalBlockId"
          :can-edit="canEdit"
          :can-annotate="canAnnotate"
          @select="activeLogicalBlockId = $event"
          @update="editBlock"
          @insert="insertBlock"
          @move="moveBlock"
          @delete="deleteBlock"
          @change-type="changeBlockType"
          @annotate="rightTab = 'annotations'; rightDrawerVisible = true"
          @invalid="MessagePlugin.warning($event)"
        />

        <aside class="right-rail">
          <t-tabs v-model="rightTab" class="right-tabs">
            <t-tab-panel value="ai" :label="t('production.documentWorkbench.ai')"><ProductionAIRunPanel :runs="runs" :tool-calls="toolCalls" :models="models" :can-edit="canEdit" @start="startRun" @decide="decideTool" @select-run="loadToolCalls" /></t-tab-panel>
            <t-tab-panel value="annotations" :label="t('production.documentWorkbench.notes')"><ProductionAnnotationPanel :annotations="annotations" :can-annotate="canAnnotate" :can-edit="canEdit" @create="createAnnotation" @resolve="resolveAnnotation" /></t-tab-panel>
            <t-tab-panel value="versions" :label="t('production.documentWorkbench.history')"><ProductionVersionPanel :versions="versions" :selected-version-id="selectedVersionId" :current-version-id="document.current_version_id ?? ''" :diff="versionDiff" @select="selectVersion" @reload="loadWorkbench(selectedVersionId)" /></t-tab-panel>
          </t-tabs>
        </aside>
      </div>

      <t-drawer v-model:visible="leftDrawerVisible" placement="left" size="min(88vw, 340px)" :header="t('production.documentWorkbench.openContext')" :footer="false">
        <ProductionOutline :blocks="draftBlocks" :active-logical-block-id="activeLogicalBlockId" @select="activeLogicalBlockId = $event" />
        <ProductionEvidencePanel :evidence="evidence" :linked-ids="activeBlock?.evidence_refs ?? []" :active-logical-block-id="activeLogicalBlockId" :can-edit="canEdit" @toggle="toggleEvidence" />
      </t-drawer>
      <t-drawer v-model:visible="rightDrawerVisible" placement="right" size="min(92vw, 400px)" :header="t('production.documentWorkbench.openTools')" :footer="false">
        <t-tabs v-model="rightTab">
          <t-tab-panel value="ai" :label="t('production.documentWorkbench.ai')"><ProductionAIRunPanel :runs="runs" :tool-calls="toolCalls" :models="models" :can-edit="canEdit" @start="startRun" @decide="decideTool" @select-run="loadToolCalls" /></t-tab-panel>
          <t-tab-panel value="annotations" :label="t('production.documentWorkbench.notes')"><ProductionAnnotationPanel :annotations="annotations" :can-annotate="canAnnotate" :can-edit="canEdit" @create="createAnnotation" @resolve="resolveAnnotation" /></t-tab-panel>
          <t-tab-panel value="versions" :label="t('production.documentWorkbench.history')"><ProductionVersionPanel :versions="versions" :selected-version-id="selectedVersionId" :current-version-id="document.current_version_id ?? ''" :diff="versionDiff" @select="selectVersion" @reload="loadWorkbench(selectedVersionId)" /></t-tab-panel>
        </t-tabs>
      </t-drawer>
    </template>
  </main>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import type { ModelConfig } from '@/api/model'
import { listModels } from '@/api/model'
import type { TenantRole } from '@/api/tenant/members'
import {
  appendProductionVersion,
  createProductionAnnotation,
  decideProductionToolCall,
  getProductionDocument,
  getProductionDocumentVersion,
  listProductionAnnotations,
  listProductionDocumentRuns,
  listProductionDocumentVersions,
  listProductionEvidence,
  listProductionProjects,
  listProductionRunToolCalls,
  startProductionDocumentRun,
  updateProductionAnnotationStatus,
  type AppendProductionVersionInput,
  type CreateProductionAnnotationInput,
  type DecideProductionToolCallInput,
  type ProductionAnnotation,
  type ProductionDocument,
  type ProductionDocumentVersionDetail,
  type ProductionDocumentVersionSummary,
  type ProductionEvidenceSnapshot,
  type ProductionProject,
  type ProductionRun,
  type ProductionRunType,
  type ProductionToolCall,
  type StartProductionDocumentRunInput,
} from '@/api/production'
import { createProductionCommand, type ProductionCommand } from '@/api/production/idempotency'
import { useAuthStore } from '@/stores/auth'
import { useProductionStore } from '@/stores/production'
import ProductionAIRunPanel from './components/ProductionAIRunPanel.vue'
import ProductionAnnotationPanel from './components/ProductionAnnotationPanel.vue'
import ProductionBlockEditor from './components/ProductionBlockEditor.vue'
import ProductionEvidencePanel from './components/ProductionEvidencePanel.vue'
import ProductionOutline from './components/ProductionOutline.vue'
import ProductionVersionPanel from './components/ProductionVersionPanel.vue'
import {
  applyDocumentBlockOperation,
  draftBlocksPayload,
  summarizeVersionDiff,
  versionDraftBlocks,
  type ProductionDraftBlock,
  type ProductionEditorBlockType,
  type ProductionVersionDiff,
} from './models/documentBlocks'
import { createLatestRequestCoordinator } from './models/latestRequestCoordinator'
import { canEditProductionProject } from './models/productionViewModel'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const store = useProductionStore()
const loadCoordinator = createLatestRequestCoordinator()
const toolCoordinator = createLatestRequestCoordinator()

const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const pageError = ref('')
const conflict = ref(false)
const document = ref<ProductionDocument | null>(null)
const project = ref<ProductionProject | null>(null)
const versions = shallowRef<ProductionDocumentVersionSummary[]>([])
const versionDetail = shallowRef<ProductionDocumentVersionDetail | null>(null)
const evidence = shallowRef<ProductionEvidenceSnapshot[]>([])
const annotations = shallowRef<ProductionAnnotation[]>([])
const runs = shallowRef<ProductionRun[]>([])
const toolCalls = shallowRef<ProductionToolCall[]>([])
const models = shallowRef<ModelConfig[]>([])
const draftBlocks = shallowRef<ProductionDraftBlock[]>([])
const versionDiff = ref<ProductionVersionDiff | null>(null)
const selectedVersionId = ref('')
const activeLogicalBlockId = ref('')
const activeRunId = ref('')
const changeSummary = ref('')
const rightTab = ref('ai')
const leftDrawerVisible = ref(false)
const rightDrawerVisible = ref(false)

const documentId = computed(() => typeof route.params.documentId === 'string' ? route.params.documentId : '')
const activeBlock = computed<ProductionDraftBlock | null>(() => draftBlocks.value.find(row => row.logical_block_id === activeLogicalBlockId.value) ?? null)
const canEditProject = computed(() => !!project.value && canEditProductionProject(auth.currentTenantRole as TenantRole | '', project.value))
const canEdit = computed(() => canEditProject.value
  && document.value?.status !== 'archived'
  && selectedVersionId.value === document.value?.current_version_id)
const selectedPersistedBlock = computed(() => versionDetail.value?.blocks.find(row => row.logical_block_id === activeLogicalBlockId.value) ?? null)
const canAnnotate = computed(() => canEdit.value && !!selectedPersistedBlock.value)
const canSave = computed(() => canEdit.value && !saving.value && draftBlocks.value.length > 0)
const saveHint = computed(() => canEdit.value ? t('production.documentWorkbench.saveVersion') : readOnlyMessage.value)
const readOnlyMessage = computed(() => {
  if (project.value?.status === 'archived') return t('production.permissions.archivedReadonly')
  if (document.value?.status === 'archived') return t('production.documentWorkbench.archivedDocument')
  if (selectedVersionId.value !== document.value?.current_version_id) return t('production.documentWorkbench.historicalReadonly')
  return t('production.permissions.editDenied')
})

let appendCommand: { signature: string; command: ProductionCommand<AppendProductionVersionInput> } | null = null
let runCommand: { signature: string; command: ProductionCommand<StartProductionDocumentRunInput> } | null = null
const toolCommands = new Map<string, ProductionCommand<DecideProductionToolCallInput>>()
const annotationStatusCommands = new Map<string, ProductionCommand<{ status: 'resolved' | 'dismissed' }>>()
let annotationCommand: { signature: string; command: ProductionCommand<CreateProductionAnnotationInput> } | null = null

function responseError(response: { message?: string }, fallback: string) { return new Error(response.message || fallback) }
function isConflictError(cause: unknown) { return typeof cause === 'object' && cause !== null && 'response' in cause && (cause as { response?: { status?: number } }).response?.status === 409 }
function stableCommand<T>(current: { signature: string; command: ProductionCommand<T> } | null, signature: string, payload: T) {
  return current?.signature === signature ? current : { signature, command: createProductionCommand(payload) }
}

async function loadWorkbench(preferredVersionId = '') {
  const requestedDocumentId = documentId.value
  if (!requestedDocumentId) return
  loading.value = true
  pageError.value = ''
  conflict.value = false
  await loadCoordinator.run(async () => {
    const [documentResponse, projectsResponse, versionsResponse, runsResponse, availableModels] = await Promise.all([
      getProductionDocument(requestedDocumentId),
      listProductionProjects(),
      listProductionDocumentVersions(requestedDocumentId),
      listProductionDocumentRuns(requestedDocumentId),
      listModels('KnowledgeQA').catch(() => []),
    ])
    if (!documentResponse.success || !documentResponse.data) throw responseError(documentResponse, t('production.documentWorkbench.loadFailed'))
    if (!projectsResponse.success || !versionsResponse.success || !runsResponse.success) throw new Error(t('production.documentWorkbench.loadFailed'))
    const documentRow = documentResponse.data
    const versionRows = versionsResponse.data ?? []
    const versionId = versionRows.some(row => row.id === preferredVersionId)
      ? preferredVersionId
      : documentRow.current_version_id || versionRows.at(-1)?.id || ''
    if (!versionId) throw new Error(t('production.documentWorkbench.noVersion'))
    const detailResponse = await getProductionDocumentVersion(requestedDocumentId, versionId)
    if (!detailResponse.success || !detailResponse.data) throw responseError(detailResponse, t('production.documentWorkbench.loadFailed'))
    const runRows = runsResponse.data ?? []
    const runId = runRows.some(row => row.id === activeRunId.value) ? activeRunId.value : runRows[0]?.id || ''
    const [evidenceResponse, annotationResponse, toolResponse, comparisonResponse] = await Promise.all([
      listProductionEvidence(detailResponse.data.source_set_id),
      listProductionAnnotations(requestedDocumentId, { version_id: versionId, page_size: 100 }),
      runId ? listProductionRunToolCalls(runId) : Promise.resolve({ success: true, data: [] as ProductionToolCall[] }),
      detailResponse.data.parent_version_id
        ? getProductionDocumentVersion(requestedDocumentId, detailResponse.data.parent_version_id).catch(() => null)
        : Promise.resolve(null),
    ])
    if (!evidenceResponse.success || !annotationResponse.success || !toolResponse.success) throw new Error(t('production.documentWorkbench.loadFailed'))
    return { documentRow, projects: projectsResponse.data ?? [], versionRows, detail: detailResponse.data, comparison: comparisonResponse?.success ? comparisonResponse.data ?? null : null, evidenceRows: evidenceResponse.data ?? [], annotationRows: annotationResponse.data ?? [], runRows, toolRows: toolResponse.data ?? [], availableModels, runId }
  }, {
    success: result => {
      document.value = result.documentRow
      project.value = result.projects.find(row => row.id === result.documentRow.project_id) ?? null
      versions.value = result.versionRows
      versionDetail.value = result.detail
      selectedVersionId.value = result.detail.id
      evidence.value = result.evidenceRows
      annotations.value = result.annotationRows
      runs.value = result.runRows
      toolCalls.value = result.toolRows
      models.value = result.availableModels
      activeRunId.value = result.runId
      draftBlocks.value = versionDraftBlocks(result.detail)
      versionDiff.value = result.comparison ? summarizeVersionDiff(result.comparison, result.detail) : null
      activeLogicalBlockId.value = draftBlocks.value[0]?.logical_block_id ?? ''
      store.upsertDocument(result.documentRow)
		store.upsertVersion(result.detail)
      store.replaceRuns(result.runRows)
      store.activeProjectId = result.documentRow.project_id
      store.activeDocumentId = result.documentRow.id
      store.activeVersionId = result.detail.id
      loaded.value = true
      appendCommand = null
    },
    error: cause => { pageError.value = cause instanceof Error ? cause.message : t('production.documentWorkbench.loadFailed') },
    settled: () => { loading.value = false },
  })
}

function applyOperation(operation: Parameters<typeof applyDocumentBlockOperation>[1]) {
  try { draftBlocks.value = applyDocumentBlockOperation(draftBlocks.value, operation) }
  catch (cause) { MessagePlugin.warning(cause instanceof Error ? cause.message : t('production.documentWorkbench.invalidBlock')) }
}
function editBlock(id: string, content: import('@/api/production').ProductionJSON) { applyOperation({ op: 'edit', logicalBlockId: id, content }) }
function insertBlock(id: string) { applyOperation({ op: 'insertAfter', after: id, blockType: 'paragraph' }) }
function moveBlock(id: string, toIndex: number) { applyOperation({ op: 'move', logicalBlockId: id, toIndex }) }
function deleteBlock(id: string) { applyOperation({ op: 'delete', logicalBlockId: id }); if (activeLogicalBlockId.value === id) activeLogicalBlockId.value = draftBlocks.value[0]?.logical_block_id ?? '' }
function changeBlockType(id: string, blockType: ProductionEditorBlockType) { applyOperation({ op: 'changeType', logicalBlockId: id, blockType }) }
function toggleEvidence(evidenceId: string, checked: boolean) { if (activeLogicalBlockId.value) applyOperation({ op: checked ? 'linkEvidence' : 'unlinkEvidence', logicalBlockId: activeLogicalBlockId.value, evidenceId }) }

async function saveVersion() {
  if (!document.value?.current_version_id || !canSave.value || !versionDetail.value) return
  saving.value = true
  pageError.value = ''
  try {
    const payload = draftBlocksPayload(draftBlocks.value, versionDetail.value.source_set_id, { changeSummary: changeSummary.value, origin: 'human' })
    const signature = JSON.stringify(payload)
    appendCommand = stableCommand(appendCommand, signature, payload)
    const response = await appendProductionVersion(document.value.id, document.value.current_version_id, appendCommand.command)
    if (!response.success || !response.data) throw responseError(response, t('production.documentWorkbench.saveFailed'))
    MessagePlugin.success(t('production.documentWorkbench.saved'))
    changeSummary.value = ''
    appendCommand = null
    await loadWorkbench(response.data.id)
  } catch (cause) {
    conflict.value = isConflictError(cause)
    pageError.value = conflict.value ? '' : cause instanceof Error ? cause.message : t('production.documentWorkbench.saveFailed')
  } finally { saving.value = false }
}

async function startRun(runType: Exclude<ProductionRunType, 'collect'>, modelId: string) {
  if (!document.value || !canEdit.value) return
  const payload = { run_type: runType, model_id: modelId } as StartProductionDocumentRunInput
  const signature = JSON.stringify(payload)
  runCommand = stableCommand(runCommand, signature, payload)
  try {
    const response = await startProductionDocumentRun(document.value.id, runCommand.command)
    if (!response.success || !response.data) throw responseError(response, t('production.documentWorkbench.runFailed'))
    runCommand = null
    await loadWorkbench(selectedVersionId.value)
  } catch (cause) { pageError.value = cause instanceof Error ? cause.message : t('production.documentWorkbench.runFailed') }
}

async function loadToolCalls(runId: string) {
  activeRunId.value = runId
  await toolCoordinator.run(() => listProductionRunToolCalls(runId), {
    success: response => { if (response.success) toolCalls.value = response.data ?? [] },
    error: cause => { pageError.value = cause instanceof Error ? cause.message : t('production.documentWorkbench.loadFailed') },
  })
}

async function decideTool(callId: string, decision: 'approve' | 'reject') {
  const key = `${callId}:${decision}`
  const command = toolCommands.get(key) ?? createProductionCommand({ decision })
  toolCommands.set(key, command)
  try {
    const response = await decideProductionToolCall(callId, command)
    if (!response.success) throw responseError(response, t('production.documentWorkbench.decisionFailed'))
    toolCommands.delete(key)
    await loadToolCalls(activeRunId.value)
  } catch (cause) { pageError.value = cause instanceof Error ? cause.message : t('production.documentWorkbench.decisionFailed') }
}

async function createAnnotation(input: { body: string; severity: ProductionAnnotation['severity'] }) {
  if (!document.value || !versionDetail.value || !selectedPersistedBlock.value) return
  const payload: CreateProductionAnnotationInput = { version_id: versionDetail.value.id, block_id: selectedPersistedBlock.value.id, annotation_type: 'comment', severity: input.severity, anchor: { logical_block_id: selectedPersistedBlock.value.logical_block_id }, body: input.body }
  const signature = JSON.stringify(payload)
  annotationCommand = stableCommand(annotationCommand, signature, payload)
  try {
    const response = await createProductionAnnotation(document.value.id, annotationCommand.command)
    if (!response.success || !response.data) throw responseError(response, t('production.documentWorkbench.annotationFailed'))
    annotations.value = [response.data, ...annotations.value]
    annotationCommand = null
  } catch (cause) { pageError.value = cause instanceof Error ? cause.message : t('production.documentWorkbench.annotationFailed') }
}

async function resolveAnnotation(id: string) {
  const command = annotationStatusCommands.get(id) ?? createProductionCommand({ status: 'resolved' as const })
  annotationStatusCommands.set(id, command)
  try {
    const response = await updateProductionAnnotationStatus(id, command)
    if (!response.success) throw responseError(response, t('production.documentWorkbench.annotationFailed'))
    annotationStatusCommands.delete(id)
    annotations.value = annotations.value.map(row => row.id === id ? { ...row, status: 'resolved' } : row)
  } catch (cause) { pageError.value = cause instanceof Error ? cause.message : t('production.documentWorkbench.annotationFailed') }
}

function selectVersion(id: string) { if (id !== selectedVersionId.value) loadWorkbench(id) }
function reloadLatest() { conflict.value = false; loadWorkbench(document.value?.current_version_id ?? '') }
function backToProject() { router.push(project.value ? { name: 'productionProject', params: { projectId: project.value.id } } : { name: 'productionProjects' }) }

watch(documentId, () => { loadCoordinator.invalidate(); toolCoordinator.invalidate(); loaded.value = false; document.value = null; loadWorkbench() })
onMounted(() => loadWorkbench())
onBeforeUnmount(() => loadCoordinator.invalidate())
onBeforeUnmount(() => toolCoordinator.invalidate())
</script>

<style scoped>
.document-workbench-page { width: 100%; height: calc(100vh - 64px); min-height: 520px; display: flex; flex-direction: column; overflow: hidden; color: var(--td-text-color-primary); background: var(--td-bg-color-container); }
.document-topbar { min-height: 56px; display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 0 16px; border-bottom: 1px solid var(--td-component-stroke); }
.document-identity, .document-actions, .responsive-tools { min-width: 0; display: flex; align-items: center; gap: 8px; }
.document-identity h1 { min-width: 0; max-width: 42vw; overflow: hidden; margin: 0; font-size: 15px; line-height: 24px; letter-spacing: 0; text-overflow: ellipsis; white-space: nowrap; }
.document-kicker { color: var(--td-text-color-secondary); font-size: 12px; white-space: nowrap; }
.topbar-rule { width: 1px; height: 20px; background: var(--td-component-stroke); }
.change-summary { width: 210px; }
.responsive-tools { display: none; }
.workbench-alert { flex: none; margin: 8px 12px 0; }
.workbench-state { flex: 1; display: flex; align-items: center; justify-content: center; gap: 10px; color: var(--td-text-color-secondary); }
.workbench-state--error { color: var(--td-error-color); }
.production-document-layout { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(220px, 280px) minmax(520px, 1fr) minmax(280px, 360px); }
.production-document-layout > :nth-child(2) { min-width: 320px; }
.left-rail, .right-rail { min-width: 0; overflow: auto; background: var(--td-bg-color-container); }
.left-rail { border-right: 1px solid var(--td-component-stroke); }
.right-rail { border-left: 1px solid var(--td-component-stroke); }
.right-tabs { height: 100%; }
.right-tabs :deep(.t-tabs__nav-wrap) { padding: 0 8px; }
.right-tabs :deep(.t-tabs__content) { padding: 0; }
@media (max-width: 1099px) {
  .production-document-layout { grid-template-columns: minmax(320px, 1fr); }
  .left-rail, .right-rail { display: none; }
  .responsive-tools { display: flex; }
  .change-summary { width: min(24vw, 190px); }
}
@media (max-width: 640px) {
  .document-workbench-page { height: calc(100dvh - 56px); min-height: 420px; }
  .document-topbar { min-height: 100px; align-items: stretch; flex-direction: column; justify-content: center; gap: 6px; padding: 8px; }
  .document-identity h1 { max-width: 48vw; }
  .document-kicker, .topbar-rule { display: none; }
  .document-actions { width: 100%; }
  .change-summary { flex: 1; width: auto; }
}
</style>
