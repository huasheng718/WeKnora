<template>
  <section class="production-panel" :aria-labelledby="`${panelId}-title`">
    <header class="panel-header">
      <div>
        <h2 :id="`${panelId}-title`">{{ t('production.documents.title') }}</h2>
        <p>{{ t('production.documents.description') }}</p>
      </div>
      <t-tooltip :content="documentActionHint">
        <span>
          <t-button size="small" :disabled="!canCreateDocument" @click="dialogVisible = true">
            <template #icon><t-icon name="add" /></template>
            {{ t('production.documents.create') }}
          </t-button>
        </span>
      </t-tooltip>
    </header>

    <div v-if="loading" class="panel-loading" aria-live="polite">
      <t-skeleton v-for="row in 4" :key="row" animation="gradient" :row-col="[{ width: '100%', height: '58px' }]" />
    </div>
    <div v-else-if="documents.length === 0" class="panel-state">
      <t-icon name="file-copy" size="28px" />
      <div>
        <strong>{{ t('production.documents.emptyTitle') }}</strong>
        <span>{{ canEdit ? t('production.documents.emptyEditable') : t('production.documents.emptyReadonly') }}</span>
      </div>
    </div>
    <div v-else class="document-list">
      <button
        v-for="document in documents"
        :key="document.id"
        type="button"
        class="document-row"
        @click="openDocument(document.id)"
      >
        <span class="document-icon" aria-hidden="true"><t-icon name="file-copy" /></span>
        <span class="document-main">
          <span class="document-title-line">
            <strong>{{ document.title }}</strong>
            <t-tag size="small" variant="light" :theme="documentTheme(document.status)">
              {{ t(`production.documentStatus.${document.status}`) }}
            </t-tag>
          </span>
          <span>{{ documentTypeName(document.document_type_id) }}</span>
        </span>
        <time :datetime="document.updated_at">{{ formatDate(document.updated_at) }}</time>
        <t-icon name="chevron-right" class="document-chevron" aria-hidden="true" />
      </button>
    </div>

    <t-dialog
      v-model:visible="dialogVisible"
      width="500px"
      :header="t('production.documents.dialogTitle')"
      :confirm-btn="{ content: t('production.actions.create'), loading: submitting, disabled: !formReady }"
      :cancel-btn="{ content: t('production.actions.cancel') }"
      :close-on-overlay-click="!submitting"
      :on-confirm="createDocument"
    >
      <t-alert v-if="dialogError" class="dialog-alert" theme="error" :message="dialogError" />
      <t-form label-align="top" :data="form" @submit.prevent>
        <t-form-item :label="t('production.fields.documentTitle')" required>
          <t-input v-model="form.title" :maxlength="255" :placeholder="t('production.documents.titlePlaceholder')" :disabled="submitting" />
        </t-form-item>
        <t-form-item :label="t('production.fields.documentType')" required>
          <t-select
            v-model="form.document_type_id"
            :options="documentTypeOptions"
            :placeholder="t('production.documents.typePlaceholder')"
            :disabled="submitting"
            @change="form.source_set_id = ''"
          />
        </t-form-item>
        <t-form-item :label="t('production.fields.sourceSet')" required>
          <t-select
            v-model="form.source_set_id"
            :options="sourceSetOptions"
            :placeholder="t('production.documents.sourcePlaceholder')"
            :disabled="submitting || !form.document_type_id"
          />
        </t-form-item>
      </t-form>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  createProductionDocument,
  type ProductionDocument,
  type ProductionDocumentStatus,
  type ProductionDocumentType,
  type ProductionSourceSet,
} from '@/api/production'
import { createProductionCommand } from '@/api/production/idempotency'
import { productionDocumentLocation } from '../models/productionViewModel'

const props = defineProps<{
  projectId: string
  documents: readonly ProductionDocument[]
  sourceSets: readonly ProductionSourceSet[]
  documentTypes: readonly ProductionDocumentType[]
  canEdit: boolean
  loading: boolean
}>()
const emit = defineEmits<{
  (event: 'created', document: ProductionDocument): void
}>()

const { t, locale } = useI18n()
const router = useRouter()
const panelId = `production-documents-${Math.random().toString(36).slice(2)}`
const dialogVisible = ref(false)
const submitting = ref(false)
const dialogError = ref('')
const form = reactive({ title: '', document_type_id: '', source_set_id: '' })

const activeTypes = computed(() => props.documentTypes.filter(row => row.status === 'active'))
const frozenSources = computed(() => props.sourceSets.filter(row => row.status === 'frozen'))
const documentTypeOptions = computed(() => activeTypes.value.map(row => ({ label: row.name, value: row.id })))
const sourceSetOptions = computed(() => frozenSources.value
  .filter(row => row.document_type_id === form.document_type_id)
  .map(row => ({ label: `${documentTypeName(row.document_type_id)} · ${formatDate(row.created_at)}`, value: row.id })))
const canCreateDocument = computed(() => props.canEdit && activeTypes.value.length > 0 && frozenSources.value.length > 0 && !props.loading)
const formReady = computed(() => form.title.trim().length > 0 && !!form.document_type_id && !!form.source_set_id)
const documentActionHint = computed(() => {
  if (!props.canEdit) return t('production.permissions.editDenied')
  if (activeTypes.value.length === 0) return t('production.documents.noActiveTypes')
  if (frozenSources.value.length === 0) return t('production.documents.noFrozenSources')
  return t('production.documents.create')
})

function documentTypeName(id: string) {
  return props.documentTypes.find(row => row.id === id)?.name ?? t('production.fields.unknownType')
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

function documentTheme(status: ProductionDocumentStatus): 'default' | 'primary' | 'success' | 'warning' {
  if (status === 'published' || status === 'approved') return 'success'
  if (status === 'in_review' || status === 'publishing') return 'warning'
  if (status === 'draft' || status === 'annotating') return 'primary'
  return 'default'
}

function openDocument(id: string) {
  router.push(productionDocumentLocation(id))
}

async function createDocument() {
  if (!canCreateDocument.value || !formReady.value || submitting.value) return
  submitting.value = true
  dialogError.value = ''
  try {
    const response = await createProductionDocument(props.projectId, createProductionCommand({
      title: form.title.trim(),
      document_type_id: form.document_type_id,
      source_set_id: form.source_set_id,
    }))
    if (!response.success || !response.data) throw new Error(response.message || t('production.errors.createDocument'))
    emit('created', response.data)
    MessagePlugin.success(t('production.messages.documentCreated'))
    dialogVisible.value = false
    Object.assign(form, { title: '', document_type_id: '', source_set_id: '' })
  } catch (cause) {
    dialogError.value = cause instanceof Error ? cause.message : t('production.errors.createDocument')
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.production-panel { min-width: 0; }
.panel-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; margin-bottom: 18px; }
.panel-header h2 { margin: 0; font-size: 18px; line-height: 26px; letter-spacing: 0; }
.panel-header p { margin: 4px 0 0; color: var(--td-text-color-secondary); font-size: 13px; line-height: 20px; }
.panel-loading { display: grid; gap: 8px; }
.panel-state { min-height: 180px; display: flex; align-items: center; justify-content: center; gap: 14px; color: var(--td-text-color-secondary); border-block: 1px solid var(--td-component-stroke); }
.panel-state > div { display: flex; flex-direction: column; gap: 3px; }
.panel-state strong { color: var(--td-text-color-primary); }
.document-list { border-top: 1px solid var(--td-component-stroke); }
.document-row { appearance: none; width: 100%; min-height: 68px; display: grid; grid-template-columns: 36px minmax(0, 1fr) minmax(120px, auto) 20px; align-items: center; gap: 12px; padding: 0; border: 0; border-bottom: 1px solid var(--td-component-stroke); background: transparent; color: inherit; text-align: left; cursor: pointer; }
.document-row:hover { background: var(--td-bg-color-container-hover); }
.document-row:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: -2px; }
.document-icon { width: 32px; height: 32px; display: grid; place-items: center; background: var(--td-bg-color-secondarycontainer); border-radius: var(--td-radius-medium); color: var(--td-text-color-secondary); }
.document-main { min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.document-title-line { display: flex; align-items: center; gap: 8px; min-width: 0; }
.document-title-line strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.document-main > span:last-child, .document-row time { color: var(--td-text-color-secondary); font-size: 12px; }
.document-row time { text-align: right; }
.document-chevron { color: var(--td-text-color-placeholder); }
.dialog-alert { margin-bottom: 16px; }
@media (max-width: 640px) {
  .panel-header { align-items: stretch; flex-direction: column; }
  .panel-header :deep(.t-button) { width: 100%; }
  .document-row { grid-template-columns: 32px minmax(0, 1fr) 20px; padding: 10px 0; }
  .document-row time { grid-column: 2; text-align: left; }
  .document-chevron { grid-column: 3; grid-row: 1 / span 2; }
}
</style>
