<template>
  <t-dialog v-model:visible="visible" width="min(920px, 94vw)" :header="t('production.releaseConsole.dialogTitle')" :confirm-btn="null" :cancel-btn="null" destroy-on-close>
    <div class="release-dialog">
      <t-alert v-if="error" theme="error" :message="error" />
      <div class="release-grid">
        <section class="target-pane">
          <header><strong>{{ t('production.releaseConsole.targets') }}</strong><span>{{ t('production.releaseConsole.selected', { count: selectedKnowledgeBaseIds.length }) }}</span></header>
          <div v-if="loading && !preflight" class="loading"><t-loading size="small" /></div>
          <label v-for="target in preflightTargets" :key="target.knowledge_base_id" class="target-row">
            <t-checkbox v-model="selectedKnowledgeBaseIds" :value="target.knowledge_base_id" :disabled="!target.ready" />
            <span><strong>{{ target.knowledge_base_name }}</strong><small>{{ target.ready ? t('production.releaseConsole.ready') : t('production.releaseConsole.notReady') }}</small></span>
            <t-tag size="small" variant="light" :theme="target.ready ? 'success' : 'warning'">{{ target.ready ? t('production.releaseConsole.ready') : target.reason }}</t-tag>
            <dl v-if="target.config_snapshot" class="config-grid">
              <template v-for="row in configRows(target.config_snapshot)" :key="row[0]"><dt>{{ row[0] }}</dt><dd>{{ row[1] }}</dd></template>
            </dl>
          </label>
          <t-button v-if="preflight?.has_more" block variant="text" :loading="loading" @click="loadPreflight((preflight?.page ?? 1) + 1, true)">{{ t('production.releaseConsole.loadMore') }}</t-button>
        </section>
        <section class="snapshot-pane"><header><strong>{{ t('production.releaseConsole.snapshot') }}</strong><t-tag size="small" variant="outline">Markdown</t-tag></header><pre>{{ preflight?.rendered_markdown }}</pre></section>
      </div>
      <footer><t-checkbox v-model="confirmed">{{ t('production.releaseConsole.confirmSnapshot') }}</t-checkbox><div><t-button variant="text" @click="visible = false">{{ t('production.actions.cancel') }}</t-button><t-button theme="primary" :loading="submitting" :disabled="!canConfirm" @click="confirmRelease"><template #icon><t-icon name="send" /></template>{{ t('production.releaseConsole.publish') }}</t-button></div></footer>
    </div>
  </t-dialog>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { createProductionRelease, getProductionReleasePreflight, type ProductionJSON, type ProductionRelease, type ProductionReleasePreflight, type ProductionReleasePreflightTarget } from '@/api/production'
import { createProductionCommand, type ProductionCommand } from '@/api/production/idempotency'
import { canConfirmProductionRelease } from '../models/releaseActions'
import { createScopedMutationCoordinator } from '../models/scopedMutationCoordinator'
const props = defineProps<{ modelValue: boolean; documentId: string; versionId: string }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: boolean): void; (event: 'created', release: ProductionRelease): void }>()
const { t } = useI18n()
const visible = computed({ get: () => props.modelValue, set: value => emit('update:modelValue', value) })
const preflight = shallowRef<ProductionReleasePreflight | null>(null); const preflightTargets = shallowRef<ProductionReleasePreflightTarget[]>([]); const selectedKnowledgeBaseIds = ref<string[]>([]); const confirmed = ref(false); const loading = ref(false); const submitting = ref(false); const error = ref('')
const mutationCoordinator = createScopedMutationCoordinator()
let releaseCommand: { signature: string; command: ProductionCommand<{ version_id: string; target_knowledge_base_ids: string[] }> } | null = null
const canConfirm = computed(() => canConfirmProductionRelease(selectedKnowledgeBaseIds.value, preflightTargets.value, confirmed.value))
async function loadPreflight(page = 1, append = false) { if (!props.documentId || !props.versionId) return; loading.value = true; error.value = ''; try { const response = await getProductionReleasePreflight(props.documentId, props.versionId, page, 100); if (!response.success || !response.data) throw new Error(response.message); preflight.value = response.data; preflightTargets.value = append ? [...preflightTargets.value, ...response.data.targets] : response.data.targets } catch (e) { error.value = e instanceof Error ? e.message : t('production.releaseConsole.loadFailed') } finally { loading.value = false } }
function configRows(value: ProductionJSON): [string, string][] { if (!value || Array.isArray(value) || typeof value !== 'object') return []; const object = value as Record<string, ProductionJSON>; const chunking = object.chunking as Record<string, ProductionJSON> | undefined; const graph = object.graph as Record<string, ProductionJSON> | undefined; const extraction = graph?.extract_config as Record<string, ProductionJSON> | undefined; return [['chunking', String(chunking?.strategy ?? '-')], ['size', String(chunking?.chunk_size ?? '-')], ['overlap', String(chunking?.overlap ?? '-')], ['separators', JSON.stringify(chunking?.separators ?? [])], ['embedding', String(object.embedding_model_id ?? '-')], ['graph', String(graph?.enabled ?? false)], ['graph model', String(graph?.model_id ?? '-')], ['extraction', JSON.stringify(extraction ?? {})]] }
async function confirmRelease() { if (!canConfirm.value) return; const signature = JSON.stringify([props.versionId, [...selectedKnowledgeBaseIds.value].sort()]); releaseCommand = releaseCommand?.signature === signature ? releaseCommand : { signature, command: createProductionCommand({ version_id: props.versionId, target_knowledge_base_ids: [...selectedKnowledgeBaseIds.value].sort() }) }; const mutation = mutationCoordinator.start(props.documentId); submitting.value = true; try { const response = await createProductionRelease(props.documentId, releaseCommand.command); if (!response.success || !response.data) throw new Error(response.message); if (mutationCoordinator.isCurrent(mutation, props.documentId)) { releaseCommand = null; emit('created', response.data); visible.value = false } } catch (e) { error.value = e instanceof Error ? e.message : t('production.releaseConsole.commandFailed') } finally { if (mutationCoordinator.isCurrent(mutation, props.documentId)) submitting.value = false } }
watch(() => [props.modelValue, props.documentId, props.versionId] as const, ([open]) => { mutationCoordinator.invalidate(); if (open) { preflight.value = null; preflightTargets.value = []; selectedKnowledgeBaseIds.value = []; confirmed.value = false; releaseCommand = null; loadPreflight() } })
onBeforeUnmount(() => mutationCoordinator.invalidate())
</script>
<style scoped>
.release-dialog { min-width: 0; }.release-grid { display: grid; grid-template-columns: minmax(300px, 1fr) minmax(320px, 1.1fr); min-height: 430px; border: 1px solid var(--td-component-stroke); }.target-pane,.snapshot-pane { min-width: 0; overflow: auto; }.target-pane { border-right: 1px solid var(--td-component-stroke); }.target-pane>header,.snapshot-pane>header { position: sticky; top: 0; z-index: 1; height: 42px; display: flex; align-items: center; justify-content: space-between; padding: 0 12px; border-bottom: 1px solid var(--td-component-stroke); background: var(--td-bg-color-container); font-size: 12px; }.target-row { min-width: 0; display: grid; grid-template-columns: auto minmax(0,1fr) auto; gap: 8px; padding: 10px 12px; border-bottom: 1px solid var(--td-component-stroke); }.target-row>span { display: grid; min-width: 0; }.target-row small { color: var(--td-text-color-secondary); }.config-grid { grid-column: 2/-1; display: grid; grid-template-columns: 86px minmax(0,1fr); gap: 3px 8px; margin: 5px 0 0; font-size: 10px; }.config-grid dt { color: var(--td-text-color-placeholder); }.config-grid dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }.snapshot-pane pre { margin: 0; padding: 14px; white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.65 ui-monospace, monospace; }.release-dialog>footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding-top: 14px; }.release-dialog>footer>div { display: flex; gap: 8px; }.loading { padding: 32px; text-align: center; }
@media (max-width: 720px) { .release-grid { grid-template-columns: 1fr; max-height: 60vh; overflow: auto; }.target-pane { border-right: 0; border-bottom: 1px solid var(--td-component-stroke); }.release-dialog>footer { align-items: flex-start; flex-direction: column; }.release-dialog>footer>div { width: 100%; justify-content: flex-end; } }
</style>
