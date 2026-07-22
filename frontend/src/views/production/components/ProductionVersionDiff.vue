<template>
  <section class="diff-strip" :aria-label="t('production.reviewConsole.versionDiff')">
    <span><b>+</b>{{ diff?.added ?? 0 }}</span>
    <span><b>~</b>{{ diff?.changed ?? 0 }}</span>
    <span><b>-</b>{{ diff?.removed ?? 0 }}</span>
    <span><b>=</b>{{ diff?.unchanged ?? 0 }}</span>
  </section>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProductionVersionDiff } from '../models/documentBlocks'
defineProps<{ diff: ProductionVersionDiff | null }>()
const { t } = useI18n()
</script>
<style scoped>
.diff-strip { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); border-bottom: 1px solid var(--td-component-stroke); background: var(--td-bg-color-secondarycontainer); }
.diff-strip span { display: flex; justify-content: center; gap: 5px; padding: 8px 4px; color: var(--td-text-color-secondary); font-size: 12px; }
.diff-strip b { color: var(--td-text-color-primary); font-family: ui-monospace, monospace; }
</style>
