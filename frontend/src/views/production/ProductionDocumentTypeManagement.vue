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
        <t-tooltip :content="canManage ? t('production.documentTypes.create') : t('production.documentTypes.readonlyHint')">
          <span>
            <t-button :disabled="!canManage || loading" @click="drawerVisible = true">
              <template #icon><t-icon name="add" /></template>
              {{ t('production.documentTypes.create') }}
            </t-button>
          </span>
        </t-tooltip>
      </div>
    </header>

    <t-alert
      v-if="!canManage"
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
        <span>{{ canManage ? t('production.documentTypes.emptyEditable') : t('production.documentTypes.emptyReadonly') }}</span>
      </div>
      <t-button v-if="canManage" size="small" @click="drawerVisible = true">
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
                <t-tooltip
                  v-if="item.status === 'draft'"
                  :content="canManage ? t('production.documentTypes.activate') : t('production.documentTypes.readonlyHint')"
                >
                  <span>
                    <t-button
                      variant="text"
                      size="small"
                      :loading="activatingId === item.id"
                      :disabled="!canManage || Boolean(activatingId)"
                      @click="confirmActivation(item)"
                    >
                      <template #icon><t-icon name="check-circle" /></template>
                      {{ t('production.documentTypes.activate') }}
                    </t-button>
                  </span>
                </t-tooltip>
                <span v-else class="muted">{{ t('production.documentTypes.noAction') }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <t-drawer
      v-model:visible="drawerVisible"
      placement="right"
      size="min(92vw, 720px)"
      :header="t('production.documentTypes.drawerTitle')"
      :footer="false"
      :close-on-overlay-click="!submitting"
      :close-btn="!submitting"
    >
      <t-alert v-if="formError" class="form-alert" theme="error" :message="formError" />
      <t-form class="document-type-form" :data="form" label-align="top" @submit.prevent>
        <div class="identity-grid">
          <t-form-item :label="t('production.documentTypes.fields.code')" required>
            <t-input v-model="form.code" :disabled="submitting" :placeholder="t('production.documentTypes.placeholders.code')" />
          </t-form-item>
          <t-form-item :label="t('production.documentTypes.fields.name')" required>
            <t-input v-model="form.name" :disabled="submitting" :placeholder="t('production.documentTypes.placeholders.name')" />
          </t-form-item>
          <t-form-item :label="t('production.documentTypes.fields.schemaVersion')" required>
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
        <t-button :loading="submitting" :disabled="!canManage" @click="submitDraft">
          <template #icon><t-icon name="save" /></template>
          {{ t('production.documentTypes.create') }}
        </t-button>
      </div>
    </t-drawer>
  </main>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import type { TenantRole } from '@/api/tenant/members'
import {
  activateProductionDocumentType,
  createProductionDocumentType,
  listProductionDocumentTypes,
  type CreateProductionDocumentTypeInput,
  type ProductionDocumentType,
  type ProductionDocumentTypeStatus,
} from '@/api/production'
import { createProductionCommand, type ProductionCommand } from '@/api/production/idempotency'
import { useAuthStore } from '@/stores/auth'
import { createLatestRequestCoordinator } from './models/latestRequestCoordinator'
import {
  canManageProductionDocumentTypes,
  documentTypeListViewState,
  parseDocumentTypeDraft,
  sortDocumentTypeVersions,
  type DocumentTypeDraftForm,
} from './models/documentTypeManagement'

const { t, locale } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const loadCoordinator = createLatestRequestCoordinator()
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const drawerVisible = ref(false)
const submitting = ref(false)
const formError = ref('')
const activatingId = ref('')
const items = shallowRef<ProductionDocumentType[]>([])
let draftCommand: { signature: string; command: ProductionCommand<CreateProductionDocumentTypeInput> } | null = null
const activationCommands = new Map<string, ProductionCommand<void>>()

const jsonFields = [
  'blockSchema', 'sourceRequirements', 'skillBindings', 'workflowPlan',
  'qualityRules', 'reviewPolicy', 'publicationPolicy',
] as const

function emptyForm(): DocumentTypeDraftForm {
  return {
    code: '', name: '', description: '', schemaVersion: 1,
    blockSchema: '{}', sourceRequirements: '{}', skillBindings: '{}', workflowPlan: '{}',
    qualityRules: '{}', reviewPolicy: '{}', publicationPolicy: '{}',
  }
}

const form = reactive<DocumentTypeDraftForm>(emptyForm())
const canManage = computed(() => canManageProductionDocumentTypes(auth.currentTenantRole as TenantRole | ''))
const documentTypes = computed(() => sortDocumentTypeVersions(items.value))
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

watch(drawerVisible, visible => {
  if (!visible) return
  Object.assign(form, emptyForm())
  formError.value = ''
  draftCommand = null
})

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

async function loadDocumentTypes() {
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
    },
    error: cause => {
      error.value = cause instanceof Error ? cause.message : t('production.documentTypes.loadFailed')
    },
    settled: () => {
      loading.value = false
    },
  })
}

async function submitDraft() {
  if (!canManage.value || submitting.value) return
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
    items.value = items.value.filter(item => item.id !== response.data?.id).concat(response.data)
    drawerVisible.value = false
    MessagePlugin.success(t('production.documentTypes.created'))
  } catch (cause) {
    formError.value = cause instanceof Error ? cause.message : t('production.documentTypes.createFailed')
  } finally {
    submitting.value = false
  }
}

function confirmActivation(item: ProductionDocumentType) {
  if (!canManage.value || item.status !== 'draft' || activatingId.value) return
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
        MessagePlugin.error(cause instanceof Error ? cause.message : t('production.documentTypes.activateFailed'))
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
.production-page { width: min(1240px, 100%); margin: 0 auto; padding: 28px 32px 48px; box-sizing: border-box; color: var(--td-text-color-primary); }
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
table { width: 100%; min-width: 940px; border-collapse: collapse; font-size: 13px; }
th, td { padding: 14px 16px; border-bottom: 1px solid var(--td-component-stroke); text-align: left; vertical-align: middle; }
th { color: var(--td-text-color-secondary); background: var(--td-bg-color-secondarycontainer); font-size: 12px; font-weight: 500; }
tbody tr:last-child td { border-bottom: 0; }
tbody tr:hover { background: var(--td-bg-color-container-hover); }
.name-cell { min-width: 150px; }
.description-cell { min-width: 210px; max-width: 340px; color: var(--td-text-color-secondary); }
.date-cell { min-width: 160px; white-space: nowrap; color: var(--td-text-color-secondary); }
.action-column { width: 104px; text-align: right; }
code { padding: 2px 6px; border-radius: 3px; background: var(--td-bg-color-secondarycontainer); color: var(--td-text-color-primary); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.muted { color: var(--td-text-color-placeholder); }
.page-state { min-height: 330px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; color: var(--td-text-color-secondary); border-block: 1px solid var(--td-component-stroke); text-align: center; }
.page-state > div { display: flex; flex-direction: column; gap: 4px; }
.page-state strong { color: var(--td-text-color-primary); font-size: 16px; }
.page-state--error { min-height: 180px; color: var(--td-error-color); }
.form-alert { margin-bottom: 18px; }
.document-type-form { padding-bottom: 82px; }
.identity-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 132px; gap: 0 14px; }
.description-field { grid-column: 1 / -1; }
.json-section { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin-top: 8px; }
.json-field { min-width: 0; }
.json-field label { display: block; margin-bottom: 8px; color: var(--td-text-color-primary); font-size: 13px; line-height: 20px; }
.json-field:last-child { grid-column: 1 / -1; }
.json-field :deep(textarea) { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; line-height: 19px; }
.drawer-actions { position: absolute; right: 0; bottom: 0; left: 0; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--td-component-stroke); background: var(--td-bg-color-container); }
@media (max-width: 720px) {
  .production-page { padding: 20px 16px 36px; }
  .page-header { flex-direction: column; gap: 18px; }
  .header-actions, .header-actions > span, .header-actions :deep(.t-button) { width: 100%; }
  .header-actions { flex-direction: column; }
  .identity-grid, .json-section { grid-template-columns: 1fr; }
  .description-field, .json-field:last-child { grid-column: auto; }
  .drawer-actions { padding: 12px 16px; }
}
</style>
