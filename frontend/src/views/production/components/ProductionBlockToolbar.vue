<template>
  <div class="block-toolbar" :aria-label="t('production.documentWorkbench.blockToolbar')">
    <t-select :value="blockType" size="small" :disabled="disabled" :options="typeOptions" @change="$emit('change-type', $event as ProductionEditorBlockType)" />
    <span class="toolbar-rule" />
    <t-tooltip :content="t('production.documentWorkbench.addBlock')"><t-button shape="square" size="small" variant="text" :disabled="disabled" @click="$emit('insert')"><t-icon name="add" /></t-button></t-tooltip>
    <t-tooltip :content="t('production.documentWorkbench.moveUp')"><t-button shape="square" size="small" variant="text" :disabled="disabled || first" @click="$emit('move-up')"><t-icon name="arrow-up" /></t-button></t-tooltip>
    <t-tooltip :content="t('production.documentWorkbench.moveDown')"><t-button shape="square" size="small" variant="text" :disabled="disabled || last" @click="$emit('move-down')"><t-icon name="arrow-down" /></t-button></t-tooltip>
    <t-tooltip :content="t('production.documentWorkbench.annotate')"><t-button shape="square" size="small" variant="text" :disabled="!canAnnotate" @click="$emit('annotate')"><t-icon name="chat" /></t-button></t-tooltip>
    <t-tooltip :content="t('production.documentWorkbench.deleteBlock')"><t-button shape="square" size="small" variant="text" theme="danger" :disabled="disabled || only" @click="$emit('delete')"><t-icon name="delete" /></t-button></t-tooltip>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProductionEditorBlockType } from '../models/documentBlocks'

defineProps<{ blockType: ProductionEditorBlockType; disabled: boolean; first: boolean; last: boolean; only: boolean; canAnnotate: boolean }>()
defineEmits<{
  (event: 'insert' | 'move-up' | 'move-down' | 'delete' | 'annotate'): void
  (event: 'change-type', type: ProductionEditorBlockType): void
}>()
const { t } = useI18n()
const types: ProductionEditorBlockType[] = ['heading', 'paragraph', 'callout', 'code', 'list', 'table', 'image']
const typeOptions = computed(() => types.map(value => ({ value, label: t(`production.documentWorkbench.blockTypes.${value}`) })))
</script>

<style scoped>
.block-toolbar { min-height: 38px; display: flex; align-items: center; gap: 1px; padding: 3px 8px; border-bottom: 1px solid var(--td-component-stroke); background: var(--td-bg-color-secondarycontainer); }
.block-toolbar :deep(.t-select__wrap) { width: 110px; }
.toolbar-rule { width: 1px; height: 20px; margin: 0 4px; background: var(--td-component-stroke); }
</style>
