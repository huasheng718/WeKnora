<template>
  <section class="side-panel">
    <header class="panel-heading"><span><t-icon name="time" /><strong>{{ t('production.documentWorkbench.versions') }}</strong></span><t-button size="small" shape="square" variant="text" @click="$emit('reload')"><t-icon name="refresh" /></t-button></header>
    <dl v-if="diff" class="version-diff">
      <div><dt>+</dt><dd>{{ diff.added }}</dd></div>
      <div><dt>~</dt><dd>{{ diff.changed }}</dd></div>
      <div><dt>-</dt><dd>{{ diff.removed }}</dd></div>
      <div><dt>=</dt><dd>{{ diff.unchanged }}</dd></div>
    </dl>
    <div class="version-list">
      <button v-for="row in ordered" :key="row.id" type="button" :class="['version-row', { active: row.id === selectedVersionId }]" @click="$emit('select', row.id)">
        <span><strong>v{{ row.version_number }}</strong><t-tag v-if="row.id === currentVersionId" size="small" variant="light" theme="success">{{ t('production.documentWorkbench.current') }}</t-tag></span>
        <small>{{ row.change_summary || t('production.documentWorkbench.noChangeSummary') }}</small>
        <time :datetime="row.created_at">{{ formatDate(row.created_at) }}</time>
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProductionDocumentVersionSummary } from '@/api/production'
import type { ProductionVersionDiff } from '../models/documentBlocks'
const props = defineProps<{ versions: readonly ProductionDocumentVersionSummary[]; selectedVersionId: string; currentVersionId: string; diff: ProductionVersionDiff | null }>()
defineEmits<{ (event: 'select', id: string): void; (event: 'reload'): void }>()
const { t, locale } = useI18n()
const ordered = computed(() => [...props.versions].sort((a, b) => b.version_number - a.version_number))
function formatDate(value: string) { return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) }
</script>

<style scoped>
.panel-heading { height: 42px; display: flex; align-items: center; justify-content: space-between; padding: 0 8px 0 12px; border-bottom: 1px solid var(--td-component-stroke); font-size: 13px; }
.panel-heading > span { display: flex; align-items: center; gap: 8px; }
.version-diff { display: grid; grid-template-columns: repeat(4, 1fr); margin: 0; padding: 8px 12px; border-bottom: 1px solid var(--td-component-stroke); background: var(--td-bg-color-secondarycontainer); }
.version-diff div { display: flex; align-items: baseline; justify-content: center; gap: 4px; }
.version-diff dt { color: var(--td-text-color-placeholder); font: 11px ui-monospace, monospace; }
.version-diff dd { margin: 0; font-size: 12px; font-weight: 600; }
.version-list { display: grid; }
.version-row { appearance: none; min-width: 0; display: grid; gap: 4px; padding: 10px 12px; border: 0; border-bottom: 1px solid var(--td-component-stroke); background: transparent; color: inherit; text-align: left; cursor: pointer; }
.version-row:hover, .version-row.active { background: var(--td-bg-color-container-hover); }
.version-row > span { display: flex; align-items: center; justify-content: space-between; }
.version-row small { overflow: hidden; color: var(--td-text-color-secondary); text-overflow: ellipsis; white-space: nowrap; }
.version-row time { color: var(--td-text-color-placeholder); font-size: 10px; }
</style>
