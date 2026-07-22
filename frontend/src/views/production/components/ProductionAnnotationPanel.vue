<template>
  <section class="side-panel">
    <header class="panel-heading"><span><t-icon name="chat" /><strong>{{ t('production.documentWorkbench.annotations') }}</strong></span><t-tag size="small" variant="light">{{ annotations.length }}</t-tag></header>
    <form v-if="canAnnotate" class="annotation-form" @submit.prevent="$emit('create')">
      <t-textarea v-model="bodyModel" :disabled="submitting" :placeholder="t('production.documentWorkbench.annotationPlaceholder')" :autosize="{ minRows: 2, maxRows: 4 }" />
      <div>
        <t-select v-model="severityModel" size="small" :disabled="submitting" :options="severityOptions" />
        <t-button size="small" type="submit" :loading="submitting" :disabled="!bodyModel.trim()">{{ t('production.documentWorkbench.addAnnotation') }}</t-button>
      </div>
    </form>
    <div v-if="annotations.length" class="annotation-list">
      <article v-for="row in annotations" :key="row.id" class="annotation-row">
        <header><t-tag size="small" variant="light" :theme="row.severity === 'blocking' ? 'danger' : row.severity === 'warning' ? 'warning' : 'default'">{{ t(`production.documentWorkbench.severity.${row.severity}`) }}</t-tag><small>{{ t(`production.documentWorkbench.annotationStatus.${row.status}`) }}</small></header>
        <p>{{ row.body }}</p>
        <t-button v-if="row.status === 'open' && canResolve(row)" size="small" variant="text" @click="$emit('resolve', row.id)">{{ t('production.documentWorkbench.resolve') }}</t-button>
      </article>
    </div>
    <div v-else class="panel-empty">{{ t('production.documentWorkbench.noAnnotations') }}</div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProductionAnnotation } from '@/api/production'

const props = defineProps<{
  annotations: readonly ProductionAnnotation[]
  canAnnotate: boolean
  canResolve: (annotation: ProductionAnnotation) => boolean
  body: string
  severity: ProductionAnnotation['severity']
  submitting: boolean
}>()
const emit = defineEmits<{
  (event: 'update:body', value: string): void
  (event: 'update:severity', value: ProductionAnnotation['severity']): void
  (event: 'create'): void
  (event: 'resolve', id: string): void
}>()
const { t } = useI18n()
const bodyModel = computed({ get: () => props.body, set: value => emit('update:body', value) })
const severityModel = computed({ get: () => props.severity, set: value => emit('update:severity', value) })
const severityOptions = computed(() => (['info', 'warning', 'blocking'] as const).map(value => ({ value, label: t(`production.documentWorkbench.severity.${value}`) })))
</script>

<style scoped>
.side-panel { min-width: 0; }
.panel-heading { height: 42px; display: flex; align-items: center; justify-content: space-between; padding: 0 12px; border-bottom: 1px solid var(--td-component-stroke); font-size: 13px; }
.panel-heading > span { display: flex; align-items: center; gap: 8px; }
.annotation-form { display: grid; gap: 8px; padding: 10px 12px; border-bottom: 1px solid var(--td-component-stroke); }
.annotation-form > div { display: flex; justify-content: space-between; gap: 8px; }
.annotation-form :deep(.t-select__wrap) { width: 110px; }
.annotation-list { display: grid; }
.annotation-row { padding: 10px 12px; border-bottom: 1px solid var(--td-component-stroke); }
.annotation-row header { display: flex; align-items: center; justify-content: space-between; }
.annotation-row small { color: var(--td-text-color-placeholder); }
.annotation-row p { margin: 7px 0 3px; font-size: 12px; line-height: 18px; overflow-wrap: anywhere; }
.panel-empty { padding: 24px 12px; color: var(--td-text-color-placeholder); font-size: 12px; text-align: center; }
</style>
