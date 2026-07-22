<template>
  <section class="tool-panel" :aria-label="t('production.documentWorkbench.evidence')">
    <header class="panel-heading">
      <span><t-icon name="link" /><strong>{{ t('production.documentWorkbench.evidence') }}</strong></span>
      <t-tag size="small" variant="light">{{ evidence.length }}</t-tag>
    </header>
    <div v-if="evidence.length" class="evidence-list">
      <label v-for="row in evidence" :key="row.id" :class="['evidence-row', { linked: linkedIds.includes(row.id) }]">
        <t-checkbox
          :checked="linkedIds.includes(row.id)"
          :disabled="!canEdit || !activeLogicalBlockId"
          @change="$emit('toggle', row.id, $event)"
        />
        <span>
          <strong>{{ evidenceTitle(row) }}</strong>
          <small>{{ row.snapshot_type }} · {{ row.content_digest.slice(0, 10) }}</small>
        </span>
      </label>
    </div>
    <div v-else class="panel-empty">{{ t('production.documentWorkbench.noEvidence') }}</div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProductionEvidenceSnapshot } from '@/api/production'

defineProps<{
  evidence: readonly ProductionEvidenceSnapshot[]
  linkedIds: readonly string[]
  activeLogicalBlockId: string
  canEdit: boolean
}>()
defineEmits<{ (event: 'toggle', evidenceId: string, checked: boolean): void }>()
const { t } = useI18n()

function evidenceTitle(row: ProductionEvidenceSnapshot) {
  if (typeof row.inline_content === 'string') return row.inline_content.slice(0, 54)
  return row.storage_path?.split('/').pop() || row.id.slice(0, 12)
}
</script>

<style scoped>
.tool-panel { min-width: 0; border-top: 1px solid var(--td-component-stroke); }
.panel-heading { height: 42px; display: flex; align-items: center; justify-content: space-between; padding: 0 12px; border-bottom: 1px solid var(--td-component-stroke); font-size: 13px; }
.panel-heading > span { display: flex; align-items: center; gap: 8px; }
.evidence-list { display: grid; gap: 1px; padding: 6px; }
.evidence-row { min-width: 0; display: grid; grid-template-columns: 24px minmax(0, 1fr); align-items: start; padding: 8px 6px; border-radius: 4px; cursor: pointer; }
.evidence-row:hover, .evidence-row.linked { background: var(--td-bg-color-container-hover); }
.evidence-row > span { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.evidence-row strong { overflow: hidden; font-size: 12px; font-weight: 500; line-height: 18px; text-overflow: ellipsis; white-space: nowrap; }
.evidence-row small { color: var(--td-text-color-placeholder); font: 10px/16px ui-monospace, monospace; }
.panel-empty { padding: 24px 12px; color: var(--td-text-color-placeholder); font-size: 12px; text-align: center; }
</style>
