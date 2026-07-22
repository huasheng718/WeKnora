<template>
  <section class="side-panel">
    <header class="panel-heading"><t-icon name="system-sum" /><strong>{{ t('production.documentWorkbench.aiRuns') }}</strong></header>
    <div class="run-form">
      <t-select v-model="modelId" size="small" :options="modelOptions" :placeholder="t('production.documentWorkbench.selectModel')" :disabled="!canEdit" />
      <t-select v-model="runType" size="small" :options="runTypeOptions" :disabled="!canEdit" />
      <t-button size="small" :disabled="!canEdit || !modelId" @click="$emit('start', runType, modelId)"><template #icon><t-icon name="play-circle" /></template>{{ t('production.documentWorkbench.startRun') }}</t-button>
    </div>
    <div v-if="toolCalls.length" class="approval-list">
      <article v-for="call in toolCalls" :key="call.id" class="approval-row">
        <header><strong>{{ call.tool_name }}</strong><t-tag size="small" variant="light" :theme="call.approval_status === 'pending' ? 'warning' : 'default'">{{ call.approval_status }}</t-tag></header>
        <small>{{ call.provider_type }} · {{ call.provider_id }}</small>
        <div v-if="call.approval_status === 'pending' && canEdit">
          <t-button size="small" variant="outline" theme="danger" @click="$emit('decide', call.id, 'reject')">{{ t('production.documentWorkbench.reject') }}</t-button>
          <t-button size="small" @click="$emit('decide', call.id, 'approve')">{{ t('production.documentWorkbench.approve') }}</t-button>
        </div>
      </article>
    </div>
    <div class="run-list">
      <article v-for="run in runs" :key="run.id" class="run-row" @click="$emit('select-run', run.id)">
        <span><strong>{{ t(`production.documentWorkbench.runTypes.${run.run_type}`) }}</strong><t-tag size="small" variant="light" :theme="run.status === 'failed' ? 'danger' : run.status === 'completed' ? 'success' : 'primary'">{{ run.status }}</t-tag></span>
        <small>{{ run.id.slice(0, 12) }} · {{ run.current_step }}</small>
      </article>
    </div>
    <div v-if="!runs.length" class="panel-empty">{{ t('production.documentWorkbench.noRuns') }}</div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ModelConfig } from '@/api/model'
import type { ProductionRun, ProductionRunType, ProductionToolCall } from '@/api/production'

const props = defineProps<{ runs: readonly ProductionRun[]; toolCalls: readonly ProductionToolCall[]; models: readonly ModelConfig[]; canEdit: boolean }>()
defineEmits<{
  (event: 'start', runType: Exclude<ProductionRunType, 'collect'>, modelId: string): void
  (event: 'decide', callId: string, decision: 'approve' | 'reject'): void
  (event: 'select-run', runId: string): void
}>()
const { t } = useI18n()
const modelId = ref('')
const runType = ref<Exclude<ProductionRunType, 'collect'>>('write')
const modelOptions = computed(() => props.models.filter(row => row.id).map(row => ({ value: row.id!, label: row.display_name || row.name })))
const runTypeOptions = computed(() => (['write', 'rewrite', 'validate'] as const).map(value => ({ value, label: t(`production.documentWorkbench.runTypes.${value}`) })))
</script>

<style scoped>
.panel-heading { height: 42px; display: flex; align-items: center; gap: 8px; padding: 0 12px; border-bottom: 1px solid var(--td-component-stroke); font-size: 13px; }
.run-form { display: grid; grid-template-columns: minmax(0, 1fr) 100px; gap: 6px; padding: 10px 12px; border-bottom: 1px solid var(--td-component-stroke); }
.run-form > :last-child { grid-column: 1 / -1; }
.approval-list { border-bottom: 1px solid var(--td-component-stroke); }
.approval-row, .run-row { display: grid; gap: 5px; padding: 10px 12px; border-bottom: 1px solid var(--td-component-stroke); }
.approval-row header, .run-row > span { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.approval-row small, .run-row small { color: var(--td-text-color-placeholder); font-size: 10px; }
.approval-row > div { display: flex; justify-content: flex-end; gap: 6px; }
.run-row { cursor: pointer; }
.run-row:hover { background: var(--td-bg-color-container-hover); }
.panel-empty { padding: 24px 12px; color: var(--td-text-color-placeholder); font-size: 12px; text-align: center; }
</style>
