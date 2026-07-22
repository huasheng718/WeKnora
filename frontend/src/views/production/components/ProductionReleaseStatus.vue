<template>
  <section class="release-status">
    <header class="status-head"><div><strong>{{ t('production.releaseConsole.title') }}</strong><span>{{ t('production.releaseConsole.history') }}</span></div><t-button v-if="canPublish" size="small" @click="$emit('publish')"><template #icon><t-icon name="send" /></template>{{ t('production.releaseConsole.newRelease') }}</t-button></header>
    <t-alert v-if="error" theme="error" :message="error" />
    <div v-if="loading && !releases.length" class="loading"><t-loading size="small" /></div>
    <div v-else-if="!releases.length" class="empty">{{ t('production.releaseConsole.empty') }}</div>
    <article v-for="release in releases" :key="release.id" class="release-row">
      <header><span><strong>{{ documentTitle(release.document_id) }}</strong><small>{{ shortId(release.id) }}</small></span><time :datetime="release.created_at">{{ formatDate(release.created_at) }}</time></header>
      <div class="target-table" role="table">
        <div v-for="target in release.targets ?? []" :key="target.id" class="target-line" role="row">
          <span class="target-id">{{ shortId(target.target_knowledge_base_id) }}</span><t-tag size="small" variant="light" :theme="targetTheme(target.status)">{{ t(`production.releaseConsole.status.${target.status}`) }}</t-tag>
          <small v-if="target.failure_reason">{{ target.failure_reason }}</small><small v-else>{{ target.is_active ? t('production.releaseConsole.activeHead') : t('production.releaseConsole.lock', { lock: target.head_lock_version ?? 0 }) }}</small>
          <div class="target-actions"><t-button v-for="action in releaseActions(target)" :key="action" size="small" variant="text" :loading="busyId === target.id" @click="runAction(target, action)">{{ t(`production.releaseConsole.actions.${action}`) }}</t-button></div>
        </div>
      </div>
    </article>
    <t-button v-if="hasMore" block variant="text" :loading="loading" @click="load(page + 1, true)">{{ t('production.releaseConsole.loadMore') }}</t-button>
  </section>
</template>
<script setup lang="ts">
import { onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { activateProductionReleaseTarget, listProductionDocumentReleases, retryProductionReleaseTarget, rollbackProductionReleaseTarget, type ProductionDocument, type ProductionRelease, type ProductionReleaseTarget } from '@/api/production'
import { createProductionCommand, type ProductionCommand } from '@/api/production/idempotency'
import { releaseActions, type ProductionReleaseAction } from '../models/releaseActions'
import { createScopedMutationCoordinator } from '../models/scopedMutationCoordinator'
const props = defineProps<{ documents: ProductionDocument[]; canPublish: boolean }>()
defineEmits<{ (event: 'publish'): void }>()
const { t, locale } = useI18n(); const releases = shallowRef<ProductionRelease[]>([]); const loading = ref(false); const error = ref(''); const page = ref(1); const hasMore = ref(false); const busyId = ref('')
const mutationCoordinator = createScopedMutationCoordinator(); const commands = new Map<string, ProductionCommand<any>>()
function shortId(value: string) { return value.slice(0, 8) }
function documentTitle(id: string) { return props.documents.find(row => row.id === id)?.title ?? shortId(id) }
function formatDate(value: string) { return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) }
function targetTheme(status: ProductionReleaseTarget['status']) { return status === 'active' || status === 'ready' ? 'success' : status === 'failed' ? 'danger' : status === 'building' ? 'primary' : 'default' }
async function load(next = 1, append = false) { const scope = props.documents.map(row => row.id).join(':'); if (!scope) { releases.value = []; return }; loading.value = true; try { const results = await Promise.all(props.documents.map(row => listProductionDocumentReleases(row.id, next, 20))); const rows = results.flatMap(result => result.data ?? []).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)); releases.value = append ? [...releases.value, ...rows] : rows; page.value = next; hasMore.value = results.some(result => result.has_more) } catch (e) { error.value = e instanceof Error ? e.message : t('production.releaseConsole.loadFailed') } finally { loading.value = false } }
async function execute(target: ProductionReleaseTarget, action: ProductionReleaseAction) { const expected_lock = target.head_lock_version ?? 0; const key = `${target.id}:${action}:${expected_lock}`; const command = commands.get(key) ?? createProductionCommand(action === 'retry' ? undefined : { expected_lock }); commands.set(key, command); const mutation = mutationCoordinator.start(target.id); busyId.value = target.id; try { if (action === 'activate') await activateProductionReleaseTarget(target.id, command); else if (action === 'retry') await retryProductionReleaseTarget(target.id, command); else await rollbackProductionReleaseTarget(target.id, command); commands.delete(key); if (mutationCoordinator.isCurrent(mutation, target.id)) await load() } catch (e) { MessagePlugin.error(e instanceof Error ? e.message : t('production.releaseConsole.commandFailed')) } finally { if (mutationCoordinator.isCurrent(mutation, target.id)) busyId.value = '' } }
function runAction(target: ProductionReleaseTarget, action: ProductionReleaseAction) { if (action !== 'rollback') { execute(target, action); return }; const dialog = DialogPlugin.confirm({ header: t('production.releaseConsole.rollbackTitle'), body: t('production.releaseConsole.rollbackBody'), onConfirm: async () => { try { await execute(target, action) } finally { dialog.destroy() } }, onCancel: () => dialog.destroy() }) }
watch(() => props.documents.map(row => row.id).join(':'), () => { mutationCoordinator.invalidate(); releases.value = []; load() }, { immediate: true })
onBeforeUnmount(() => mutationCoordinator.invalidate())
</script>
<style scoped>
.release-status { min-width: 0; border-top: 1px solid var(--td-component-stroke); }.status-head { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 0; }.status-head>div { display: grid; }.status-head span,.release-row small,.target-line small { color: var(--td-text-color-secondary); font-size: 11px; }.release-row { border-top: 1px solid var(--td-component-stroke); }.release-row>header { display: flex; justify-content: space-between; gap: 12px; padding: 10px 8px; }.release-row>header span { display: flex; gap: 8px; }.release-row time { color: var(--td-text-color-placeholder); font-size: 11px; }.target-line { min-width: 0; display: grid; grid-template-columns: minmax(90px, .8fr) 92px minmax(140px, 1.5fr) auto; align-items: center; gap: 10px; padding: 8px; border-top: 1px solid var(--td-component-stroke); background: var(--td-bg-color-secondarycontainer); }.target-id { overflow: hidden; font: 11px ui-monospace, monospace; text-overflow: ellipsis; }.target-actions { display: flex; }.empty,.loading { padding: 52px 12px; color: var(--td-text-color-secondary); text-align: center; }
@media (max-width: 680px) { .target-line { grid-template-columns: minmax(0,1fr) auto; }.target-line small,.target-actions { grid-column: 1/-1; }.release-row>header { flex-direction: column; } }
</style>
