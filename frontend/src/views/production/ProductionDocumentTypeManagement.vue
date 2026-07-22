<template>
  <main class="production-page document-types-page">
    <header class="page-header">
      <div class="page-heading">
        <t-button class="back-button" variant="text" size="small" @click="backToProjects">
          <template #icon><t-icon name="chevron-left" /></template>
          {{ t('production.actions.backToProjects') }}
        </t-button>
        <p class="page-kicker">{{ t('production.workspace') }}</p>
        <h1>{{ t('production.documentTypes.title') }}</h1>
        <p>{{ t('production.documentTypes.description') }}</p>
      </div>
      <div class="header-actions">
        <t-button variant="outline" :loading="loading" @click="loadDocumentTypes">
          <template #icon><t-icon name="refresh" /></template>
          {{ t('production.actions.refresh') }}
        </t-button>
        <t-tooltip v-if="pageControls.create" :content="t('production.documentTypes.create')">
          <span>
            <t-button :disabled="loading" @click="openCreateDrawer">
              <template #icon><t-icon name="add" /></template>
              {{ t('production.documentTypes.create') }}
            </t-button>
          </span>
        </t-tooltip>
      </div>
    </header>

    <t-alert
      v-if="!pageControls.create"
      class="readonly-alert"
      theme="info"
      :message="t('production.documentTypes.readonlyHint')"
    />

    <div class="list-toolbar" aria-live="polite">
      <span>{{ t('production.documentTypes.count', { count: documentTypes.length }) }}</span>
      <span>{{ activeCountLabel }}</span>
    </div>

    <div v-if="viewState === 'loading'" class="loading-state" aria-live="polite">
      <t-skeleton animation="gradient" :row-col="skeletonRows" />
    </div>

    <div v-else-if="viewState === 'error'" class="page-state page-state--error" role="alert">
      <t-icon name="error-circle" size="28px" />
      <div>
        <strong>{{ t('production.documentTypes.loadFailedTitle') }}</strong>
        <span>{{ error }}</span>
      </div>
      <t-button variant="outline" size="small" @click="loadDocumentTypes">
        {{ t('production.actions.retry') }}
      </t-button>
    </div>

    <div v-else-if="viewState === 'empty'" class="page-state">
      <t-icon name="file-setting" size="34px" />
      <div>
        <strong>{{ t('production.documentTypes.emptyTitle') }}</strong>
        <span>{{ pageControls.create ? t('production.documentTypes.emptyEditable') : t('production.documentTypes.emptyReadonly') }}</span>
      </div>
      <t-button v-if="pageControls.create" size="small" @click="openCreateDrawer">
        <template #icon><t-icon name="add" /></template>
        {{ t('production.documentTypes.create') }}
      </t-button>
    </div>

    <section v-else class="table-panel" :aria-label="t('production.documentTypes.title')">
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>{{ t('production.documentTypes.fields.name') }}</th>
              <th>{{ t('production.documentTypes.fields.origin') }}</th>
              <th>{{ t('production.documentTypes.fields.code') }}</th>
              <th>{{ t('production.documentTypes.fields.schemaVersion') }}</th>
              <th>{{ t('production.documentTypes.fields.status') }}</th>
              <th>{{ t('production.fields.description') }}</th>
              <th>{{ t('production.metrics.updated') }}</th>
              <th class="action-column">{{ t('production.documentTypes.fields.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in documentTypes" :key="item.id">
              <td class="name-cell"><strong>{{ item.name }}</strong></td>
              <td>
                <t-tag size="small" variant="light" :theme="documentTypeOriginBadge(item.origin).theme">
                  {{ t(documentTypeOriginBadge(item.origin).textKey) }}
                </t-tag>
              </td>
              <td><code>{{ item.code }}</code></td>
              <td>v{{ item.schema_version }}</td>
              <td>
                <t-tag size="small" variant="light" :theme="statusTheme(item.status)">
                  {{ t(`production.documentTypes.status.${item.status}`) }}
                </t-tag>
              </td>
              <td class="description-cell">{{ item.description || t('production.projects.noDescription') }}</td>
              <td class="date-cell">{{ formatDate(item.updated_at) }}</td>
              <td class="action-column">
                <div class="row-actions">
                  <t-button variant="text" size="small" @click="openConfigurationDrawer(item)">
                    <template #icon><t-icon name="view-list" /></template>
                    {{ t('production.documentTypes.viewConfiguration') }}
                  </t-button>
                  <t-tooltip
                    v-if="rowControls(item.status).derive"
                    :content="t('production.documentTypes.deriveDraft')"
                  >
                    <span>
                      <t-button
                        variant="text"
                        size="small"
                        :disabled="submitting"
                        @click="openDeriveDrawer(item)"
                      >
                        <template #icon><t-icon name="git-branch" /></template>
                        {{ t('production.documentTypes.deriveDraft') }}
                      </t-button>
                    </span>
                  </t-tooltip>
                  <t-tooltip
                    v-if="rowControls(item.status).activate"
                    :content="t('production.documentTypes.activate')"
                  >
                    <span>
                      <t-button
                        variant="text"
                        size="small"
                        :loading="activatingId === item.id"
                        :disabled="Boolean(activatingId)"
                        @click="confirmActivation(item)"
                      >
                        <template #icon><t-icon name="check-circle" /></template>
                        {{ t('production.documentTypes.activate') }}
                      </t-button>
                    </span>
                  </t-tooltip>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <t-drawer
      v-model:visible="configurationDrawerVisible"
      placement="right"
      size="min(94vw, 760px)"
      :header="t('production.documentTypes.configurationTitle')"
      :footer="false"
    >
      <template v-if="inspectedDocumentType && inspectionSummary">
        <div class="inspection-identity">
          <div>
            <strong>{{ inspectedDocumentType.name }}</strong>
            <span>{{ inspectedDocumentType.description || t('production.projects.noDescription') }}</span>
          </div>
          <div class="inspection-tags">
            <t-tag size="small" variant="light" :theme="documentTypeOriginBadge(inspectedDocumentType.origin).theme">
              {{ t(documentTypeOriginBadge(inspectedDocumentType.origin).textKey) }}
            </t-tag>
            <t-tag size="small" variant="light" :theme="statusTheme(inspectedDocumentType.status)">
              {{ t(`production.documentTypes.status.${inspectedDocumentType.status}`) }}
            </t-tag>
          </div>
        </div>

        <section class="inspection-section" :aria-label="t('production.documentTypes.summaryTitle')">
          <h2>{{ t('production.documentTypes.summaryTitle') }}</h2>
          <dl class="summary-grid">
            <div>
              <dt>{{ t('production.documentTypes.fields.blockSchema') }}</dt>
              <dd>{{ t('production.documentTypes.summaries.sections', { count: inspectionSummary.sections.length, names: summaryList(inspectionSummary.sections) }) }}</dd>
            </div>
            <div>
              <dt>{{ t('production.documentTypes.fields.sourceRequirements') }}</dt>
              <dd>{{ t('production.documentTypes.summaries.sources', { minimum: inspectionSummary.minimumEvidence ?? t('production.documentTypes.notConfigured'), kinds: summaryList(inspectionSummary.sourceKinds), evidence: requirementLabel(inspectionSummary.requiresEvidenceSection) }) }}</dd>
            </div>
            <div>
              <dt>{{ t('production.documentTypes.fields.reviewPolicy') }}</dt>
              <dd>{{ t('production.documentTypes.summaries.review', { steps: summaryList(inspectionSummary.reviewSteps) }) }}</dd>
            </div>
            <div>
              <dt>{{ t('production.documentTypes.fields.publicationPolicy') }}</dt>
              <dd>{{ t('production.documentTypes.summaries.publication', { target: inspectionSummary.publicationTarget || t('production.documentTypes.notConfigured'), approval: requirementLabel(inspectionSummary.requiresApprovedReview) }) }}</dd>
            </div>
          </dl>
        </section>

        <section class="inspection-section raw-config-section" :aria-label="t('production.documentTypes.rawConfigurationTitle')">
          <h2>{{ t('production.documentTypes.rawConfigurationTitle') }}</h2>
          <div v-for="field in rawConfigFields" :key="field.apiField" class="raw-config-panel">
            <h3>{{ t(`production.documentTypes.fields.${field.formField}`) }}</h3>
            <pre class="raw-config-json">{{ rawConfiguration(inspectedDocumentType, field.apiField) }}</pre>
          </div>
        </section>
      </template>
    </t-drawer>

    <t-drawer
      v-model:visible="drawerVisible"
      placement="right"
      size="min(92vw, 720px)"
      :header="drawerMode === 'derive' ? t('production.documentTypes.deriveTitle') : t('production.documentTypes.drawerTitle')"
      :footer="false"
      :close-on-overlay-click="!submitting"
      :close-btn="!submitting"
    >
      <t-alert
        v-if="drawerMode === 'derive' && !deriveBase"
        class="form-alert"
        theme="warning"
        :message="t('production.documentTypes.deriveBaseUnavailable')"
      />
      <t-alert v-if="formError" class="form-alert" theme="error" :message="formError" />
      <p v-if="formError && deriveRequestFailed && drawerMode === 'derive'" class="retry-hint">{{ t('production.documentTypes.deriveRetryHint') }}</p>
      <section v-if="drawerMode === 'derive' && deriveBase" class="lineage-section" :aria-label="t('production.documentTypes.lineageTitle')">
        <h2>{{ t('production.documentTypes.lineageTitle') }}</h2>
        <dl class="lineage-grid">
          <div><dt>{{ t('production.documentTypes.fields.code') }}</dt><dd><code>{{ deriveBase.code }}</code></dd></div>
          <div><dt>{{ t('production.documentTypes.fields.templateKey') }}</dt><dd><code>{{ deriveBase.template_key || t('production.documentTypes.notConfigured') }}</code></dd></div>
          <div><dt>{{ t('production.documentTypes.baseVersion') }}</dt><dd>v{{ deriveBase.schema_version }}</dd></div>
          <div><dt>{{ t('production.documentTypes.fields.schemaVersion') }}</dt><dd>{{ t('production.documentTypes.generatedVersion') }}</dd></div>
        </dl>
      </section>
      <t-form class="document-type-form" :data="form" label-align="top" @submit.prevent>
        <div class="identity-grid" :class="{ 'identity-grid--derive': drawerMode === 'derive' }">
          <t-form-item v-if="drawerMode === 'create'" :label="t('production.documentTypes.fields.code')" required>
            <t-input v-model="form.code" :disabled="submitting" :placeholder="t('production.documentTypes.placeholders.code')" />
          </t-form-item>
          <t-form-item :label="t('production.documentTypes.fields.name')" required>
            <t-input v-model="form.name" :disabled="submitting" :placeholder="t('production.documentTypes.placeholders.name')" />
          </t-form-item>
          <t-form-item v-if="drawerMode === 'create'" :label="t('production.documentTypes.fields.schemaVersion')" required>
            <t-input-number v-model="form.schemaVersion" :disabled="submitting" :min="1" :decimal-places="0" theme="column" />
          </t-form-item>
          <t-form-item class="description-field" :label="t('production.fields.description')">
            <t-input v-model="form.description" :disabled="submitting" :placeholder="t('production.documentTypes.placeholders.description')" />
          </t-form-item>
        </div>

        <div class="json-section">
          <div v-for="field in jsonFields" :key="field" class="json-field">
            <label :for="field">{{ t(`production.documentTypes.fields.${field}`) }}</label>
            <t-textarea
              :id="field"
              v-model="form[field]"
              :disabled="submitting"
              :autosize="{ minRows: 4, maxRows: 10 }"
              spellcheck="false"
            />
          </div>
        </div>
      </t-form>
      <div class="drawer-actions">
        <t-button variant="outline" :disabled="submitting" @click="drawerVisible = false">
          {{ t('production.actions.cancel') }}
        </t-button>
        <t-button v-if="pageControls.create && (drawerMode === 'create' || deriveBase)" :loading="submitting" @click="submitDraft">
          <template #icon><t-icon name="save" /></template>
          {{ drawerMode === 'derive' ? t('production.documentTypes.deriveDraft') : t('production.documentTypes.create') }}
        </t-button>
      </div>
    </t-drawer>
  </main>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import type { TenantRole } from '@/api/tenant/members'
import {
  activateProductionDocumentType,
  createProductionDocumentType,
  deriveProductionDocumentType,
  listProductionDocumentTypes,
  type CreateProductionDocumentTypeInput,
  type DeriveProductionDocumentTypeInput,
  type ProductionDocumentType,
  type ProductionDocumentTypeStatus,
  type ProductionJSON,
} from '@/api/production'
import { createProductionCommand, type ProductionCommand } from '@/api/production/idempotency'
import { useAuthStore } from '@/stores/auth'
import { createLatestRequestCoordinator } from './models/latestRequestCoordinator'
import {
  DocumentTypeDerivationLifecycle,
  documentTypeControlVisibility,
  documentTypeConfigurationSummary,
  documentTypeListViewState,
  documentTypeOriginBadge,
  formatDocumentTypeJSON,
  parseDerivedDocumentTypeDraft,
  parseDocumentTypeDraft,
  prefillDerivedDocumentTypeForm,
  productionRequestFailure,
  sortDocumentTypeVersions,
  type DocumentTypeDraftForm,
} from './models/documentTypeManagement'

type DocumentTypeDrawerMode = 'create' | 'derive'

const { t, locale } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const loadCoordinator = createLatestRequestCoordinator()
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const drawerVisible = ref(false)
const drawerMode = ref<DocumentTypeDrawerMode>('create')
const deriveBase = shallowRef<ProductionDocumentType | null>(null)
const configurationDrawerVisible = ref(false)
const inspectedDocumentType = shallowRef<ProductionDocumentType | null>(null)
const submitting = ref(false)
const formError = ref('')
const deriveRequestFailed = ref(false)
const activatingId = ref('')
const items = shallowRef<ProductionDocumentType[]>([])
let draftCommand: { signature: string; command: ProductionCommand<CreateProductionDocumentTypeInput> } | null = null
const derivationLifecycle = new DocumentTypeDerivationLifecycle<ProductionCommand<DeriveProductionDocumentTypeInput>>()
const activationCommands = new Map<string, ProductionCommand<void>>()

const jsonFields = [
  'blockSchema', 'sourceRequirements', 'skillBindings', 'workflowPlan',
  'qualityRules', 'reviewPolicy', 'publicationPolicy',
] as const
const rawConfigFields = [
  { formField: 'blockSchema', apiField: 'block_schema' },
  { formField: 'sourceRequirements', apiField: 'source_requirements' },
  { formField: 'skillBindings', apiField: 'skill_bindings' },
  { formField: 'workflowPlan', apiField: 'workflow_plan' },
  { formField: 'qualityRules', apiField: 'quality_rules' },
  { formField: 'reviewPolicy', apiField: 'review_policy' },
  { formField: 'publicationPolicy', apiField: 'publication_policy' },
] as const
type RawConfigurationField = (typeof rawConfigFields)[number]['apiField']

function emptyForm(): DocumentTypeDraftForm {
  return {
    code: '', name: '', description: '', schemaVersion: 1,
    blockSchema: '{}', sourceRequirements: '{}', skillBindings: '{}', workflowPlan: '{}',
    qualityRules: '{}', reviewPolicy: '{}', publicationPolicy: '{}',
  }
}

const form = reactive<DocumentTypeDraftForm>(emptyForm())
const currentRole = computed(() => auth.currentTenantRole as TenantRole | '')
const pageControls = computed(() => documentTypeControlVisibility(currentRole.value))
const documentTypes = computed(() => sortDocumentTypeVersions(items.value))
const inspectionSummary = computed(() => inspectedDocumentType.value
  ? documentTypeConfigurationSummary(inspectedDocumentType.value)
  : null)
const viewState = computed(() => documentTypeListViewState({
  loading: loading.value,
  loaded: loaded.value,
  error: error.value,
  itemCount: documentTypes.value.length,
}))
const activeCountLabel = computed(() => t('production.documentTypes.activeCount', {
  count: documentTypes.value.filter(item => item.status === 'active').length,
}))
const skeletonRows = [
  { width: '100%', height: '42px' }, { width: '100%', height: '56px' },
  { width: '100%', height: '56px' }, { width: '100%', height: '56px' },
]

function openCreateDrawer() {
  if (!pageControls.value.create) return
  drawerMode.value = 'create'
  deriveBase.value = null
  Object.assign(form, emptyForm())
  formError.value = ''
  deriveRequestFailed.value = false
  draftCommand = null
  derivationLifecycle.reset()
  drawerVisible.value = true
}

function openDeriveDrawer(item: ProductionDocumentType) {
  if (!rowControls(item.status).derive) return
  drawerMode.value = 'derive'
  deriveBase.value = item
  Object.assign(form, prefillDerivedDocumentTypeForm(item))
  formError.value = ''
  deriveRequestFailed.value = false
  draftCommand = null
  derivationLifecycle.reset()
  drawerVisible.value = true
}

function rowControls(status: ProductionDocumentTypeStatus) {
  return documentTypeControlVisibility(currentRole.value, status)
}

function openConfigurationDrawer(item: ProductionDocumentType) {
  inspectedDocumentType.value = item
  configurationDrawerVisible.value = true
}

function summaryList(values: string[]) {
  return values.length > 0 ? values.join(' · ') : t('production.documentTypes.notConfigured')
}

function requirementLabel(required: boolean) {
  return required ? t('production.documentTypes.requiredValue') : t('production.documentTypes.notRequiredValue')
}

function rawConfiguration(item: ProductionDocumentType, field: RawConfigurationField) {
  return formatDocumentTypeJSON(item[field] as ProductionJSON)
}

function backToProjects() {
  router.push({ name: 'productionProjects' })
}

function statusTheme(status: ProductionDocumentTypeStatus): 'default' | 'success' | 'warning' {
  if (status === 'active') return 'success'
  if (status === 'draft') return 'warning'
  return 'default'
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

async function loadDocumentTypes(): Promise<ProductionDocumentType[] | null> {
  let result: ProductionDocumentType[] | null = null
  loading.value = true
  error.value = ''
  await loadCoordinator.run(async () => {
    const response = await listProductionDocumentTypes()
    if (!response.success) throw new Error(response.message || t('production.documentTypes.loadFailed'))
    return response.data ?? []
  }, {
    success: value => {
      items.value = value
      loaded.value = true
      result = value
    },
    error: cause => {
      error.value = productionRequestFailure(cause, t('production.documentTypes.loadFailed')).message
    },
    settled: () => {
      loading.value = false
    },
  })
  return result
}

async function submitDraft() {
  if (!pageControls.value.create || submitting.value) return
  if (drawerMode.value === 'derive') {
    await submitDerivedDraft()
    return
  }

  formError.value = ''
  const parsed = parseDocumentTypeDraft(form)
  if (!parsed.ok) {
    const field = t(`production.documentTypes.fields.${parsed.field}`)
    formError.value = parsed.reason === 'invalid_json'
      ? t('production.documentTypes.invalidJson', { field })
      : parsed.reason === 'schema_version'
        ? t('production.documentTypes.invalidSchemaVersion')
        : t('production.documentTypes.required', { field })
    return
  }

  submitting.value = true
  const signature = JSON.stringify(parsed.payload)
  draftCommand = draftCommand?.signature === signature
    ? draftCommand
    : { signature, command: createProductionCommand(parsed.payload) }
  try {
    const response = await createProductionDocumentType(draftCommand.command)
    if (!response.success || !response.data) throw new Error(response.message || t('production.documentTypes.createFailed'))
    draftCommand = null
    await loadDocumentTypes()
    drawerVisible.value = false
    MessagePlugin.success(t('production.documentTypes.created'))
  } catch (cause) {
    formError.value = productionRequestFailure(cause, t('production.documentTypes.createFailed')).message
  } finally {
    submitting.value = false
  }
}

async function submitDerivedDraft() {
  deriveRequestFailed.value = false
  if (!deriveBase.value) {
    derivationLifecycle.reset()
    formError.value = t('production.documentTypes.deriveBaseUnavailable')
    return
  }
  const base = deriveBase.value
  formError.value = ''
  const parsed = parseDerivedDocumentTypeDraft(form)
  if (!parsed.ok) {
    const field = t(`production.documentTypes.fields.${parsed.field}`)
    formError.value = parsed.reason === 'invalid_json'
      ? t('production.documentTypes.invalidJson', { field })
      : t('production.documentTypes.required', { field })
    return
  }

  submitting.value = true
  const baseID = base.id
  const command = derivationLifecycle.prepare(
    baseID,
    parsed.payload,
    () => createProductionCommand(parsed.payload),
  )
  try {
    const response = await deriveProductionDocumentType(baseID, command)
    if (!response.success || !response.data) throw new Error(response.message || t('production.documentTypes.deriveFailed'))
    await derivationLifecycle.succeed(loadDocumentTypes)
    drawerVisible.value = false
    MessagePlugin.success(t('production.documentTypes.derived'))
  } catch (cause) {
    const failure = await derivationLifecycle.fail(
      cause,
      t('production.documentTypes.deriveFailed'),
      form,
      base,
      loadDocumentTypes,
    )
    formError.value = failure.message
    deriveRequestFailed.value = failure.command !== null
    if (failure.status === 409 && failure.refreshed) deriveBase.value = failure.base
  } finally {
    submitting.value = false
  }
}

function confirmActivation(item: ProductionDocumentType) {
  if (!rowControls(item.status).activate || activatingId.value) return
  const dialog = DialogPlugin.confirm({
    header: t('production.documentTypes.activateTitle'),
    body: t('production.documentTypes.activateBody', { name: item.name, version: item.schema_version }),
    confirmBtn: t('production.documentTypes.activate'),
    cancelBtn: t('production.actions.cancel'),
    onConfirm: async () => {
      activatingId.value = item.id
      const command = activationCommands.get(item.id) ?? createProductionCommand(undefined)
      activationCommands.set(item.id, command)
      try {
        const response = await activateProductionDocumentType(item.id, command)
        if (!response.success || !response.data) throw new Error(response.message || t('production.documentTypes.activateFailed'))
        activationCommands.delete(item.id)
        await loadDocumentTypes()
        MessagePlugin.success(t('production.documentTypes.activated'))
      } catch (cause) {
        MessagePlugin.error(productionRequestFailure(cause, t('production.documentTypes.activateFailed')).message)
      } finally {
        activatingId.value = ''
        dialog.destroy()
      }
    },
    onCancel: () => dialog.destroy(),
  })
}

onMounted(loadDocumentTypes)
onBeforeUnmount(() => {
  loadCoordinator.invalidate()
  activationCommands.clear()
})
</script>

<style scoped>
.production-page { width: min(1240px, 100%); max-width: 100%; margin: 0 auto; padding: 28px 32px 48px; box-sizing: border-box; overflow-x: hidden; color: var(--td-text-color-primary); }
.page-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 32px; padding-bottom: 24px; border-bottom: 1px solid var(--td-component-stroke); }
.page-heading { min-width: 0; }
.back-button { margin: -6px 0 10px -8px; color: var(--td-text-color-secondary); }
.page-kicker { margin: 0 0 5px; color: var(--td-brand-color); font-size: 12px; font-weight: 600; text-transform: uppercase; }
.page-header h1 { margin: 0; font-size: 26px; line-height: 34px; letter-spacing: 0; }
.page-header p:not(.page-kicker) { max-width: 700px; margin: 6px 0 0; color: var(--td-text-color-secondary); font-size: 14px; line-height: 22px; }
.header-actions { display: flex; align-items: center; gap: 10px; flex: 0 0 auto; }
.readonly-alert { margin-top: 16px; }
.list-toolbar { min-height: 52px; display: flex; align-items: center; justify-content: space-between; gap: 16px; color: var(--td-text-color-secondary); font-size: 13px; }
.loading-state { padding: 18px; border: 1px solid var(--td-component-stroke); border-radius: var(--td-radius-medium); }
.table-panel { border: 1px solid var(--td-component-stroke); border-radius: var(--td-radius-medium); background: var(--td-bg-color-container); overflow: hidden; }
.table-scroll { max-width: 100%; overflow-x: auto; }
table { width: 100%; min-width: 1180px; border-collapse: collapse; font-size: 13px; }
th, td { padding: 14px 16px; border-bottom: 1px solid var(--td-component-stroke); text-align: left; vertical-align: middle; }
th { color: var(--td-text-color-secondary); background: var(--td-bg-color-secondarycontainer); font-size: 12px; font-weight: 500; }
tbody tr:last-child td { border-bottom: 0; }
tbody tr:hover { background: var(--td-bg-color-container-hover); }
.name-cell { min-width: 150px; }
.description-cell { min-width: 210px; max-width: 340px; color: var(--td-text-color-secondary); }
.date-cell { min-width: 160px; white-space: nowrap; color: var(--td-text-color-secondary); }
.action-column { width: 292px; text-align: right; }
.row-actions { min-width: 276px; display: flex; align-items: center; justify-content: flex-end; gap: 2px; }
code { padding: 2px 6px; border-radius: 3px; background: var(--td-bg-color-secondarycontainer); color: var(--td-text-color-primary); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.muted { color: var(--td-text-color-placeholder); }
.page-state { min-height: 330px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; color: var(--td-text-color-secondary); border-block: 1px solid var(--td-component-stroke); text-align: center; }
.page-state > div { display: flex; flex-direction: column; gap: 4px; }
.page-state strong { color: var(--td-text-color-primary); font-size: 16px; }
.page-state--error { min-height: 180px; color: var(--td-error-color); }
.form-alert { margin-bottom: 18px; }
.retry-hint { margin: -10px 0 18px; color: var(--td-text-color-secondary); font-size: 12px; line-height: 19px; }
.document-type-form { padding-bottom: 82px; }
.identity-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 132px; gap: 0 14px; }
.identity-grid--derive { grid-template-columns: minmax(0, 1fr); }
.description-field { grid-column: 1 / -1; }
.json-section { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin-top: 8px; }
.json-field { min-width: 0; }
.json-field label { display: block; margin-bottom: 8px; color: var(--td-text-color-primary); font-size: 13px; line-height: 20px; }
.json-field:last-child { grid-column: 1 / -1; }
.json-field :deep(textarea) { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; line-height: 19px; }
.inspection-identity { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; padding-bottom: 18px; border-bottom: 1px solid var(--td-component-stroke); }
.inspection-identity > div:first-child { min-width: 0; display: flex; flex-direction: column; gap: 5px; }
.inspection-identity strong { overflow-wrap: anywhere; font-size: 16px; line-height: 24px; }
.inspection-identity span { color: var(--td-text-color-secondary); font-size: 13px; line-height: 20px; overflow-wrap: anywhere; }
.inspection-tags { display: flex; flex: 0 0 auto; gap: 6px; }
.inspection-section { padding: 20px 0 4px; }
.inspection-section h2, .lineage-section h2 { margin: 0 0 12px; font-size: 14px; line-height: 22px; }
.summary-grid, .lineage-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); margin: 0; border-top: 1px solid var(--td-component-stroke); }
.summary-grid > div, .lineage-grid > div { min-width: 0; padding: 12px 14px; border-bottom: 1px solid var(--td-component-stroke); }
.summary-grid > div:nth-child(odd), .lineage-grid > div:nth-child(odd) { border-right: 1px solid var(--td-component-stroke); }
.summary-grid dt, .lineage-grid dt { margin-bottom: 4px; color: var(--td-text-color-secondary); font-size: 12px; line-height: 18px; }
.summary-grid dd, .lineage-grid dd { margin: 0; font-size: 13px; line-height: 21px; overflow-wrap: anywhere; }
.raw-config-section { padding-bottom: 24px; }
.raw-config-panel { border-top: 1px solid var(--td-component-stroke); }
.raw-config-panel:last-child { border-bottom: 1px solid var(--td-component-stroke); }
.raw-config-panel h3 { margin: 0; padding: 11px 0 7px; font-size: 13px; line-height: 20px; }
.raw-config-json { max-width: 100%; margin: 0; padding: 0 0 14px; overflow-wrap: anywhere; white-space: pre-wrap; word-break: break-word; color: var(--td-text-color-secondary); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; line-height: 19px; }
.lineage-section { margin-bottom: 18px; padding: 14px 0 4px; border-bottom: 1px solid var(--td-component-stroke); }
.lineage-grid { margin-bottom: 12px; }
.drawer-actions { position: absolute; right: 0; bottom: 0; left: 0; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--td-component-stroke); background: var(--td-bg-color-container); }
@media (max-width: 720px) {
  .production-page { padding: 20px 16px 36px; }
  .page-header { flex-direction: column; gap: 18px; }
  .header-actions, .header-actions > span, .header-actions :deep(.t-button) { width: 100%; }
  .header-actions { flex-direction: column; }
  .identity-grid, .json-section { grid-template-columns: 1fr; }
  .description-field, .json-field:last-child { grid-column: auto; }
  .inspection-identity { flex-direction: column; gap: 12px; }
  .summary-grid, .lineage-grid { grid-template-columns: 1fr; }
  .summary-grid > div:nth-child(odd), .lineage-grid > div:nth-child(odd) { border-right: 0; }
  .drawer-actions { padding: 12px 16px; }
}
</style>
