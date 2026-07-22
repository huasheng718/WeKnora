<template>
  <section class="review-panel">
    <header class="panel-head"><span><t-icon name="check-double" /><strong>{{ t('production.reviewConsole.title') }}</strong></span><t-button shape="square" variant="text" :aria-label="t('production.actions.refresh')" :loading="loading" @click="load(1)"><t-icon name="refresh" /></t-button></header>
    <div class="review-summary">
      <t-tag size="small" variant="light" :theme="frozen ? 'success' : 'warning'">{{ frozen ? t('production.reviewConsole.frozen') : t('production.reviewConsole.notFrozen') }}</t-tag>
      <span>{{ t('production.reviewConsole.openBlocking', { count: openBlocking }) }}</span>
      <t-tooltip :content="submitReason"><span><t-button size="small" :disabled="!submitGate.allowed" :loading="submitting" @click="submit"><template #icon><t-icon name="send" /></template>{{ t('production.reviewConsole.submit') }}</t-button></span></t-tooltip>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div v-if="!reviews.length && !loading" class="empty">{{ t('production.reviewConsole.empty') }}</div>
    <article v-for="review in reviews" :key="review.id" class="review-row">
      <div class="review-meta"><t-tag size="small" variant="light">{{ t(`production.reviewConsole.status.${review.status}`) }}</t-tag><time :datetime="review.submitted_at">{{ formatDate(review.submitted_at) }}</time></div>
      <ol class="steps">
        <li v-for="step in review.steps ?? []" :key="step.id">
          <div><strong>{{ t(`production.reviewConsole.roles.${step.required_role}`) }}</strong><small>{{ step.reviewer_user_id || t('production.reviewConsole.unassigned') }}</small></div>
          <t-tag size="small" variant="outline">{{ t(`production.reviewConsole.decision.${step.decision}`) }}</t-tag>
          <p v-if="step.comment">{{ step.comment }}</p>
          <div v-if="reviewStepActions(tenantRole, projectRoles, review.status, step).length" class="step-actions">
            <t-button v-for="action in reviewStepActions(tenantRole, projectRoles, review.status, step)" :key="action" size="small" variant="text" @click="decide(review, step, action)">{{ t(`production.reviewConsole.actions.${action}`) }}</t-button>
          </div>
        </li>
      </ol>
      <div v-if="reviewTerminalActions(tenantRole, review.status).length" class="terminal-actions">
        <t-button v-for="action in reviewTerminalActions(tenantRole, review.status)" :key="action" size="small" theme="danger" variant="text" @click="terminal(review, action)">{{ t(`production.reviewConsole.actions.${action}`) }}</t-button>
      </div>
    </article>
    <t-button v-if="hasMore" block size="small" variant="text" :loading="loading" @click="load(page + 1, true)">{{ t('production.reviewConsole.loadMore') }}</t-button>
  </section>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import type { TenantRole } from '@/api/tenant/members'
import { cancelProductionReview, decideProductionReviewStep, listProductionDocumentReviews, rejectProductionReview, submitProductionReview, type ProductionProjectRole, type ProductionReview, type ProductionReviewStep } from '@/api/production'
import { createProductionCommand, type ProductionCommand } from '@/api/production/idempotency'
import { reviewStepActions, reviewSubmitGate, reviewTerminalActions } from '../models/reviewGate'
import { createLatestRequestCoordinator } from '../models/latestRequestCoordinator'
import { commandForReviewVersion, type ReviewVersionCommand } from '../models/reviewLifecycle'
import { createScopedMutationCoordinator } from '../models/scopedMutationCoordinator'
const props = defineProps<{ documentId: string; versionId: string; frozen: boolean; current: boolean; openBlocking: number; tenantRole: TenantRole | ''; projectRoles: ProductionProjectRole[] }>()
const emit = defineEmits<{ (event: 'changed'): void }>()
const { t, locale } = useI18n()
const reviews = shallowRef<ProductionReview[]>([]); const loading = ref(false); const submitting = ref(false); const error = ref(''); const page = ref(1); const hasMore = ref(false)
const mutationCoordinator = createScopedMutationCoordinator()
const loadCoordinator = createLatestRequestCoordinator()
let submitCommand: ReviewVersionCommand<ProductionCommand<{ version_id: string }>> | null = null
const commands = new Map<string, ProductionCommand<any>>()
const pending = computed(() => reviews.value.find(row => row.status === 'pending'))
const submitGate = computed(() => reviewSubmitGate({ openBlocking: props.openBlocking, frozen: props.frozen, current: props.current, reviewStatus: pending.value?.status, tenantRole: props.tenantRole, projectRoles: props.projectRoles }))
const submitReason = computed(() => submitGate.value.reason ? t(`production.reviewConsole.gates.${submitGate.value.reason}`) : t('production.reviewConsole.submit'))
function formatDate(value: string) { return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) }
async function load(next = 1, append = false) { const documentId = props.documentId; if (!documentId) return; loading.value = true; error.value = ''; await loadCoordinator.run(async () => { const response = await listProductionDocumentReviews(documentId, next, 20); if (!response.success) throw new Error(response.message || t('production.reviewConsole.loadFailed')); return response }, { success: response => { if (documentId !== props.documentId) return; reviews.value = append ? [...reviews.value, ...(response.data ?? [])] : (response.data ?? []); page.value = next; hasMore.value = response.has_more }, error: cause => { if (documentId !== props.documentId) return; error.value = cause instanceof Error ? cause.message : t('production.reviewConsole.loadFailed') }, settled: () => { if (documentId === props.documentId) loading.value = false } }) }
async function submit() { if (!submitGate.value.allowed) return; const mutation = mutationCoordinator.start(props.documentId); submitCommand = commandForReviewVersion(props.versionId, submitCommand, () => createProductionCommand({ version_id: props.versionId })); submitting.value = true; try { await submitProductionReview(props.documentId, submitCommand.command); if (mutationCoordinator.isCurrent(mutation, props.documentId)) { submitCommand = null; await load(); emit('changed') } } catch (e) { if (mutationCoordinator.isCurrent(mutation, props.documentId)) error.value = e instanceof Error ? e.message : t('production.reviewConsole.commandFailed') } finally { if (mutationCoordinator.isCurrent(mutation, props.documentId)) submitting.value = false } }
async function decide(review: ProductionReview, step: ProductionReviewStep, decision: 'approved' | 'changes_requested' | 'rejected') { const documentId = props.documentId; const comment = decision === 'approved' ? '' : t('production.reviewConsole.defaultComment'); const key = `${step.id}:${decision}:${comment}`; const command = commands.get(key) ?? createProductionCommand({ decision, comment }); commands.set(key, command); try { await decideProductionReviewStep(review.id, step.id, command); commands.delete(key); if (documentId !== props.documentId) return; await load(); emit('changed') } catch (e) { if (documentId === props.documentId) MessagePlugin.error(e instanceof Error ? e.message : t('production.reviewConsole.commandFailed')) } }
function terminal(review: ProductionReview, action: 'reject' | 'cancel') { const documentId = props.documentId; const dialog = DialogPlugin.confirm({ header: t(`production.reviewConsole.confirm.${action}`), body: t('production.reviewConsole.confirm.body'), onConfirm: async () => { if (documentId !== props.documentId) { dialog.destroy(); return }; const reason = t('production.reviewConsole.defaultComment'); const key = `${review.id}:${action}:${reason}`; const command = commands.get(key) ?? createProductionCommand({ reason }); commands.set(key, command); try { if (action === 'reject') await rejectProductionReview(review.id, command); else await cancelProductionReview(review.id, command); commands.delete(key); if (documentId !== props.documentId) return; await load(); emit('changed') } catch (e) { if (documentId === props.documentId) MessagePlugin.error(e instanceof Error ? e.message : t('production.reviewConsole.commandFailed')) } finally { dialog.destroy() } }, onCancel: () => dialog.destroy() }) }
watch(() => props.documentId, () => { mutationCoordinator.invalidate(); loadCoordinator.invalidate(); loading.value = false; submitting.value = false; error.value = ''; reviews.value = []; submitCommand = null; load() }, { immediate: true })
watch(() => props.versionId, () => { mutationCoordinator.invalidate(); submitting.value = false; error.value = ''; submitCommand = null })
onBeforeUnmount(() => { mutationCoordinator.invalidate(); loadCoordinator.invalidate() })
</script>
<style scoped>
.review-panel { min-width: 0; border-bottom: 1px solid var(--td-component-stroke); }
.panel-head,.panel-head span,.review-summary,.review-meta,.step-actions,.terminal-actions { display: flex; align-items: center; }
.panel-head { height: 42px; justify-content: space-between; padding: 0 8px 0 12px; border-bottom: 1px solid var(--td-component-stroke); }.panel-head span { gap: 8px; }
.review-summary { flex-wrap: wrap; gap: 8px; padding: 10px 12px; color: var(--td-text-color-secondary); font-size: 12px; }.review-summary .t-button { margin-left: auto; }
.review-row { padding: 12px; border-top: 1px solid var(--td-component-stroke); }.review-meta { justify-content: space-between; color: var(--td-text-color-placeholder); font-size: 11px; }
.steps { display: grid; gap: 8px; margin: 10px 0 0; padding: 0; list-style: none; }.steps li { display: grid; grid-template-columns: minmax(0,1fr) auto; gap: 4px 8px; }.steps li div:first-child { min-width: 0; display: grid; }.steps small,.steps p { color: var(--td-text-color-secondary); font-size: 11px; }.steps p,.step-actions { grid-column: 1/-1; margin: 0; }.terminal-actions { justify-content: flex-end; margin-top: 8px; }.empty,.error { padding: 16px 12px; color: var(--td-text-color-secondary); font-size: 12px; }.error { color: var(--td-error-color); }
</style>
