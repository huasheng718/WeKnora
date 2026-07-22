<template>
  <section class="editor" :aria-label="t('production.documentWorkbench.editor')">
    <div v-if="blocks.length" class="block-stack">
      <article
        v-for="(block, index) in blocks"
        :key="block.logical_block_id"
        :class="['editor-block', { active: block.logical_block_id === activeLogicalBlockId }]"
        @click="$emit('select', block.logical_block_id)"
      >
        <ProductionBlockToolbar
          v-if="block.logical_block_id === activeLogicalBlockId"
          :block-type="block.block_type"
          :disabled="!canEdit"
          :first="index === 0"
          :last="index === blocks.length - 1"
          :only="blocks.length === 1"
          :can-annotate="canAnnotate"
          @insert="$emit('insert', block.logical_block_id)"
          @move-up="$emit('move', block.logical_block_id, index - 1)"
          @move-down="$emit('move', block.logical_block_id, index + 1)"
          @delete="$emit('delete', block.logical_block_id)"
          @change-type="$emit('change-type', block.logical_block_id, $event)"
          @annotate="$emit('annotate')"
        />
        <textarea
          :class="['block-input', `block-input--${block.block_type}`]"
          :value="blockText(block)"
          :readonly="!canEdit"
          :aria-label="t(`production.documentWorkbench.blockTypes.${block.block_type}`)"
          @change="updateBlock(block, ($event.target as HTMLTextAreaElement).value)"
        />
        <footer v-if="block.evidence_refs.length" class="block-evidence">
          <t-icon name="link" /> {{ t('production.documentWorkbench.linkedEvidence', { count: block.evidence_refs.length }) }}
        </footer>
      </article>
    </div>
    <div v-else class="editor-empty">{{ t('production.documentWorkbench.noBlocks') }}</div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProductionJSON } from '@/api/production'
import type { ProductionDraftBlock, ProductionEditorBlockType } from '../models/documentBlocks'
import ProductionBlockToolbar from './ProductionBlockToolbar.vue'

const props = defineProps<{
  blocks: readonly ProductionDraftBlock[]
  activeLogicalBlockId: string
  canEdit: boolean
  canAnnotate: boolean
}>()
const emit = defineEmits<{
  (event: 'select', logicalBlockId: string): void
  (event: 'update', logicalBlockId: string, content: ProductionJSON): void
  (event: 'insert', logicalBlockId: string): void
  (event: 'move', logicalBlockId: string, toIndex: number): void
  (event: 'delete', logicalBlockId: string): void
  (event: 'change-type', logicalBlockId: string, type: ProductionEditorBlockType): void
  (event: 'annotate'): void
  (event: 'invalid', message: string): void
}>()
const { t } = useI18n()

function blockText(block: ProductionDraftBlock) {
  if (typeof block.content === 'string') return block.content
  if (block.block_type === 'list' && Array.isArray(block.content)) return block.content.join('\n')
  return JSON.stringify(block.content, null, 2)
}

function updateBlock(block: ProductionDraftBlock, value: string) {
  if (!props.canEdit) return
  let content: ProductionJSON = value
  if (block.block_type === 'list') content = value.split('\n').map(row => row.trim()).filter(Boolean)
  if (block.block_type === 'table' || block.block_type === 'image') {
    try { content = JSON.parse(value) as ProductionJSON } catch { emit('invalid', t('production.documentWorkbench.invalidStructuredBlock')); return }
  }
  emit('update', block.logical_block_id, content)
}
</script>

<style scoped>
.editor { min-width: 320px; height: 100%; overflow: auto; background: var(--td-bg-color-page); }
.block-stack { width: min(760px, calc(100% - 40px)); display: grid; gap: 12px; margin: 20px auto 64px; }
.editor-block { min-width: 0; border: 1px solid var(--td-component-stroke); border-radius: 4px; background: var(--td-bg-color-container); transition: border-color .15s ease, box-shadow .15s ease; }
.editor-block:hover { border-color: var(--td-border-level-2-color); }
.editor-block.active { border-color: var(--td-brand-color); box-shadow: 0 0 0 1px var(--td-brand-color-light); }
.block-input { width: 100%; min-height: 92px; display: block; resize: vertical; box-sizing: border-box; padding: 16px 18px; border: 0; outline: 0; background: transparent; color: var(--td-text-color-primary); font: 14px/1.75 TencentSans, sans-serif; letter-spacing: 0; }
.block-input--heading { min-height: 72px; font-size: 22px; font-weight: 650; line-height: 1.4; }
.block-input--code { background: var(--td-bg-color-secondarycontainer); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; }
.block-input--callout { border-left: 3px solid var(--td-warning-color); }
.block-evidence { min-height: 30px; display: flex; align-items: center; gap: 5px; padding: 0 18px; border-top: 1px solid var(--td-component-stroke); color: var(--td-text-color-placeholder); font-size: 11px; }
.editor-empty { height: 100%; display: grid; place-items: center; color: var(--td-text-color-placeholder); }
@media (max-width: 640px) { .block-stack { width: calc(100% - 16px); margin-top: 8px; } }
</style>
