<template>
  <section class="production-panel" :aria-labelledby="`${panelId}-title`">
    <header class="panel-header">
      <div>
        <h2 :id="`${panelId}-title`">{{ t('production.sources.title') }}</h2>
        <p>{{ t('production.sources.description') }}</p>
      </div>
      <t-tooltip :content="sourceActionHint">
        <span>
          <t-button size="small" :disabled="!canCreateSource" @click="dialogVisible = true">
            <template #icon><t-icon name="add" /></template>
            {{ t('production.sources.create') }}
          </t-button>
        </span>
      </t-tooltip>
    </header>

    <div v-if="loading" class="panel-loading" aria-live="polite">
      <t-skeleton v-for="row in 3" :key="row" animation="gradient" :row-col="[{ width: '100%', height: '58px' }]" />
    </div>
    <div v-else-if="sourceSets.length === 0" class="panel-state">
      <t-icon name="folder-open" size="28px" />
      <div>
        <strong>{{ t('production.sources.emptyTitle') }}</strong>
        <span>{{ canEdit ? t('production.sources.emptyEditable') : t('production.sources.emptyReadonly') }}</span>
      </div>
    </div>
    <div v-else class="source-list">
      <article v-for="sourceSet in sourceSets" :key="sourceSet.id" class="source-row">
        <div class="source-icon" aria-hidden="true"><t-icon name="folder" /></div>
        <div class="source-main">
          <div class="source-title-line">
            <strong>{{ documentTypeName(sourceSet.document_type_id) }}</strong>
            <t-tag size="small" variant="light" :theme="sourceTheme(sourceSet.status)">
              {{ t(`production.sourceStatus.${sourceSet.status}`) }}
            </t-tag>
          </div>
          <span>{{ sourceRange(sourceSet) }}</span>
        </div>
        <time :datetime="sourceSet.created_at">{{ formatDate(sourceSet.created_at) }}</time>
      </article>
    </div>

    <t-dialog
      v-model:visible="dialogVisible"
      width="480px"
      :header="t('production.sources.dialogTitle')"
      :confirm-btn="{ content: t('production.actions.create'), loading: submitting, disabled: !form.document_type_id }"
      :cancel-btn="{ content: t('production.actions.cancel') }"
      :close-on-overlay-click="!submitting"
      :on-confirm="createSourceSet"
    >
      <t-alert v-if="dialogError" class="dialog-alert" theme="error" :message="dialogError" />
      <t-form label-align="top" :data="form" @submit.prevent>
        <t-form-item :label="t('production.fields.documentType')" required>
          <t-select
            v-model="form.document_type_id"
            :options="documentTypeOptions"
            :placeholder="t('production.sources.typePlaceholder')"
            :disabled="submitting"
          />
        </t-form-item>
      </t-form>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  createProductionSourceSet,
  type ProductionDocumentType,
  type ProductionSourceSet,
  type ProductionSourceSetStatus,
} from '@/api/production'
import { createProductionCommand } from '@/api/production/idempotency'

const props = defineProps<{
  projectId: string
  sourceSets: readonly ProductionSourceSet[]
  documentTypes: readonly ProductionDocumentType[]
  canEdit: boolean
  loading: boolean
}>()
const emit = defineEmits<{
  (event: 'created', sourceSet: ProductionSourceSet): void
}>()

const { t, locale } = useI18n()
const panelId = `production-sources-${Math.random().toString(36).slice(2)}`
const dialogVisible = ref(false)
const submitting = ref(false)
const dialogError = ref('')
const form = reactive({ document_type_id: '' })

const activeDocumentTypes = computed(() => props.documentTypes.filter(row => row.status === 'active'))
const documentTypeOptions = computed(() => activeDocumentTypes.value.map(row => ({ label: row.name, value: row.id })))
const canCreateSource = computed(() => props.canEdit && activeDocumentTypes.value.length > 0 && !props.loading)
const sourceActionHint = computed(() => {
  if (!props.canEdit) return t('production.permissions.editDenied')
  if (activeDocumentTypes.value.length === 0) return t('production.sources.noActiveTypes')
  return t('production.sources.create')
})

function documentTypeName(id: string) {
  return props.documentTypes.find(row => row.id === id)?.name ?? t('production.fields.unknownType')
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

function sourceRange(sourceSet: ProductionSourceSet) {
  if (!sourceSet.time_range_start && !sourceSet.time_range_end) return t('production.sources.allTime')
  return t('production.sources.range', {
    start: sourceSet.time_range_start ? formatDate(sourceSet.time_range_start) : t('production.sources.openRange'),
    end: sourceSet.time_range_end ? formatDate(sourceSet.time_range_end) : t('production.sources.openRange'),
  })
}

function sourceTheme(status: ProductionSourceSetStatus): 'default' | 'primary' | 'success' | 'danger' {
  if (status === 'frozen') return 'primary'
  if (status === 'ready') return 'success'
  if (status === 'failed') return 'danger'
  return 'default'
}

async function createSourceSet() {
  if (!canCreateSource.value || !form.document_type_id || submitting.value) return
  submitting.value = true
  dialogError.value = ''
  try {
    const response = await createProductionSourceSet(props.projectId, createProductionCommand({
      document_type_id: form.document_type_id,
    }))
    if (!response.success || !response.data) throw new Error(response.message || t('production.errors.createSource'))
    emit('created', response.data)
    MessagePlugin.success(t('production.messages.sourceCreated'))
    dialogVisible.value = false
    form.document_type_id = ''
  } catch (cause) {
    dialogError.value = cause instanceof Error ? cause.message : t('production.errors.createSource')
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
.source-list { border-top: 1px solid var(--td-component-stroke); }
.source-row { display: grid; grid-template-columns: 36px minmax(0, 1fr) minmax(120px, auto); align-items: center; gap: 12px; min-height: 68px; border-bottom: 1px solid var(--td-component-stroke); }
.source-icon { width: 32px; height: 32px; display: grid; place-items: center; background: var(--td-bg-color-secondarycontainer); border-radius: var(--td-radius-medium); color: var(--td-text-color-secondary); }
.source-main { min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.source-title-line { display: flex; align-items: center; gap: 8px; min-width: 0; }
.source-title-line strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.source-main > span, .source-row time { color: var(--td-text-color-secondary); font-size: 12px; }
.source-row time { text-align: right; }
.dialog-alert { margin-bottom: 16px; }
@media (max-width: 640px) {
  .panel-header { align-items: stretch; flex-direction: column; }
  .panel-header :deep(.t-button) { width: 100%; }
  .source-row { grid-template-columns: 32px minmax(0, 1fr); padding: 10px 0; }
  .source-row time { grid-column: 2; text-align: left; }
}
</style>
