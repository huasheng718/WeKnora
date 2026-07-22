<template>
  <section class="tool-panel" :aria-label="t('production.documentWorkbench.outline')">
    <header class="panel-heading"><t-icon name="view-list" /><strong>{{ t('production.documentWorkbench.outline') }}</strong></header>
    <nav v-if="blocks.length" class="outline-list">
      <button
        v-for="(block, index) in blocks"
        :key="block.logical_block_id"
        type="button"
        :class="['outline-row', { active: block.logical_block_id === activeLogicalBlockId }]"
        @click="$emit('select', block.logical_block_id)"
      >
        <span>{{ index + 1 }}</span>
        <span><small>{{ blockLabel(block.block_type) }}</small><strong>{{ blockPreview(block) }}</strong></span>
      </button>
    </nav>
    <div v-else class="panel-empty">{{ t('production.documentWorkbench.noBlocks') }}</div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProductionDraftBlock } from '../models/documentBlocks'

defineProps<{ blocks: readonly ProductionDraftBlock[]; activeLogicalBlockId: string }>()
defineEmits<{ (event: 'select', logicalBlockId: string): void }>()
const { t } = useI18n()

function blockLabel(type: string) { return t(`production.documentWorkbench.blockTypes.${type}`) }
function blockPreview(block: ProductionDraftBlock) {
  if (typeof block.content === 'string') return block.content.trim() || t('production.documentWorkbench.emptyBlock')
  if (Array.isArray(block.content)) return block.content.join(' · ') || t('production.documentWorkbench.emptyBlock')
  return block.block_type === 'image'
    ? String((block.content as { alt?: string }).alt || t('production.documentWorkbench.emptyBlock'))
    : t('production.documentWorkbench.structuredBlock')
}
</script>

<style scoped>
.tool-panel { min-width: 0; }
.panel-heading { height: 42px; display: flex; align-items: center; gap: 8px; padding: 0 12px; border-bottom: 1px solid var(--td-component-stroke); font-size: 13px; }
.outline-list { display: grid; padding: 6px; }
.outline-row { appearance: none; min-width: 0; display: grid; grid-template-columns: 24px minmax(0, 1fr); align-items: start; gap: 4px; padding: 8px 6px; border: 0; border-radius: 4px; background: transparent; color: inherit; text-align: left; cursor: pointer; }
.outline-row:hover { background: var(--td-bg-color-container-hover); }
.outline-row.active { background: var(--td-brand-color-light); color: var(--td-brand-color); }
.outline-row > span:first-child { color: var(--td-text-color-placeholder); font: 11px/18px ui-monospace, monospace; }
.outline-row > span:last-child { min-width: 0; display: flex; flex-direction: column; }
.outline-row small { color: var(--td-text-color-placeholder); font-size: 10px; line-height: 16px; }
.outline-row strong { overflow: hidden; color: var(--td-text-color-primary); font-size: 12px; font-weight: 500; line-height: 18px; text-overflow: ellipsis; white-space: nowrap; }
.panel-empty { padding: 24px 12px; color: var(--td-text-color-placeholder); font-size: 12px; text-align: center; }
</style>
