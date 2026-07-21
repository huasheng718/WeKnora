<template>
  <main class="workbench-page">
    <div class="workbench-nav">
      <t-tooltip :content="t('production.actions.backToProjects')">
        <t-button shape="square" variant="text" :aria-label="t('production.actions.backToProjects')" @click="backToProjects">
          <t-icon name="chevron-left" />
        </t-button>
      </t-tooltip>
      <span>{{ t('production.workspace') }}</span>
    </div>

    <div v-if="loading && !loaded" class="workbench-loading" aria-live="polite">
      <t-skeleton animation="gradient" :row-col="[{ width: '42%', height: '30px' }, { width: '72%', height: '18px' }, { width: '100%', height: '48px' }, { width: '100%', height: '220px' }]" />
    </div>

    <div v-else-if="pageError" class="workbench-state workbench-state--error" role="alert">
      <t-icon name="error-circle" size="30px" />
      <div><strong>{{ t('production.errors.loadWorkbenchTitle') }}</strong><span>{{ pageError }}</span></div>
      <t-button size="small" variant="outline" @click="loadWorkbench">{{ t('production.actions.retry') }}</t-button>
    </div>

    <div v-else-if="!project" class="workbench-state">
      <t-icon name="folder-open" size="34px" />
      <div><strong>{{ t('production.projects.notFoundTitle') }}</strong><span>{{ t('production.projects.notFound') }}</span></div>
      <t-button size="small" variant="outline" @click="backToProjects">{{ t('production.actions.backToProjects') }}</t-button>
    </div>

    <template v-else>
      <header class="workbench-header">
        <div class="workbench-title">
          <div class="title-line">
            <h1>{{ project.name }}</h1>
            <t-tag size="small" variant="light" :theme="project.status === 'active' ? 'success' : 'default'">
              {{ t(`production.projectStatus.${project.status}`) }}
            </t-tag>
          </div>
          <p>{{ project.description || t('production.projects.noDescription') }}</p>
        </div>
        <dl class="workbench-metrics">
          <div><dt>{{ t('production.metrics.documents') }}</dt><dd>{{ documents.length }}</dd></div>
          <div><dt>{{ t('production.metrics.sourceSets') }}</dt><dd>{{ sourceSets.length }}</dd></div>
          <div><dt>{{ t('production.metrics.updated') }}</dt><dd>{{ formatDate(project.updated_at) }}</dd></div>
        </dl>
      </header>

      <t-alert
        v-if="project.status === 'archived'"
        class="archived-alert"
        theme="info"
        :message="t('production.permissions.archivedReadonly')"
      />

      <t-tabs v-model="activeTab" class="production-project-tabs">
        <t-tab-panel value="sources" :label="t('production.tabs.sources')">
          <div class="tab-content">
            <ProductionSourcePanel
              :project-id="project.id"
              :source-sets="sourceSets"
              :document-types="documentTypes"
              :can-edit="canEdit"
              :loading="loading"
              @retry="loadWorkbench"
              @created="onSourceCreated"
            />
          </div>
        </t-tab-panel>
        <t-tab-panel value="documents" :label="t('production.tabs.documents')">
          <div class="tab-content">
            <ProductionDocumentList
              :project-id="project.id"
              :documents="documents"
              :source-sets="sourceSets"
              :document-types="documentTypes"
              :can-edit="canEdit"
              :loading="loading"
              @retry="loadWorkbench"
              @created="onDocumentCreated"
            />
          </div>
        </t-tab-panel>
        <t-tab-panel value="reviews" :label="t('production.tabs.reviews')">
          <div class="tab-content tab-state">
            <t-icon name="check-double" size="30px" />
            <strong>{{ t('production.reviews.emptyTitle') }}</strong>
            <span>{{ t('production.reviews.empty') }}</span>
          </div>
        </t-tab-panel>
        <t-tab-panel value="releases" :label="t('production.tabs.releases')">
          <div class="tab-content tab-state">
            <t-icon name="send" size="30px" />
            <strong>{{ t('production.releases.emptyTitle') }}</strong>
            <span>{{ t('production.releases.empty') }}</span>
          </div>
        </t-tab-panel>
      </t-tabs>
    </template>
  </main>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import type { TenantRole } from '@/api/tenant/members'
import {
  listProductionDocuments,
  listProductionDocumentTypes,
  listProductionProjects,
  listProductionSourceSets,
  type ProductionDocument,
  type ProductionSourceSet,
} from '@/api/production'
import { useAuthStore } from '@/stores/auth'
import { useProductionStore } from '@/stores/production'
import { productionAccess } from './models/productionAccess'
import ProductionDocumentList from './components/ProductionDocumentList.vue'
import ProductionSourcePanel from './components/ProductionSourcePanel.vue'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const store = useProductionStore()
const activeTab = ref('sources')
const loading = ref(false)
const loaded = ref(false)
const pageError = ref('')

const projectId = computed(() => typeof route.params.projectId === 'string' ? route.params.projectId : '')
const project = computed(() => store.projectsById[projectId.value] ?? null)
const sourceSets = computed(() => Object.values(store.sourceSetsById).filter(row => row.project_id === projectId.value))
const documents = computed(() => Object.values(store.documentsById).filter(row => row.project_id === projectId.value))
const documentTypes = computed(() => Object.values(store.documentTypesById))
const projectRoles = computed(() => project.value?.owner_user_id === String(auth.currentUserId) ? ['project_owner'] as const : [] as const)
const canEdit = computed(() => project.value?.status === 'active'
  && productionAccess(auth.currentTenantRole as TenantRole | '', projectRoles.value).edit)

function formatDate(value: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}

function backToProjects() {
  router.push({ name: 'productionProjects' })
}

function onSourceCreated(sourceSet: ProductionSourceSet) {
  store.upsertSourceSet(sourceSet)
}

function onDocumentCreated(document: ProductionDocument) {
  store.upsertDocument(document)
}

async function loadWorkbench() {
  if (!projectId.value || loading.value) return
  loading.value = true
  pageError.value = ''
  try {
    const [projectsResponse, sourcesResponse, documentsResponse, typesResponse] = await Promise.all([
      listProductionProjects(),
      listProductionSourceSets(projectId.value),
      listProductionDocuments(projectId.value),
      listProductionDocumentTypes(),
    ])
    if (!projectsResponse.success || !sourcesResponse.success || !documentsResponse.success || !typesResponse.success) {
      throw new Error(t('production.errors.loadWorkbench'))
    }
    store.replaceProjects(projectsResponse.data ?? [])
    store.replaceSourceSets(sourcesResponse.data ?? [])
    store.replaceDocuments(documentsResponse.data ?? [])
    store.replaceDocumentTypes(typesResponse.data ?? [])
    store.activeProjectId = projectId.value
    loaded.value = true
  } catch (cause) {
    pageError.value = cause instanceof Error ? cause.message : t('production.errors.loadWorkbench')
  } finally {
    loading.value = false
  }
}

watch(projectId, () => {
  loaded.value = false
  loadWorkbench()
})
onMounted(loadWorkbench)
</script>

<style scoped>
.workbench-page { width: min(1240px, 100%); margin: 0 auto; padding: 20px 32px 48px; box-sizing: border-box; color: var(--td-text-color-primary); }
.workbench-nav { min-height: 36px; display: flex; align-items: center; gap: 6px; color: var(--td-text-color-secondary); font-size: 13px; }
.workbench-header { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: end; gap: 32px; padding: 18px 0 24px; border-bottom: 1px solid var(--td-component-stroke); }
.title-line { display: flex; align-items: center; gap: 10px; min-width: 0; }
.title-line h1 { margin: 0; overflow-wrap: anywhere; font-size: 26px; line-height: 34px; letter-spacing: 0; }
.workbench-title p { max-width: 720px; margin: 6px 0 0; color: var(--td-text-color-secondary); font-size: 14px; line-height: 22px; }
.workbench-metrics { display: grid; grid-template-columns: 70px 70px minmax(130px, auto); gap: 16px; margin: 0; }
.workbench-metrics div { padding-left: 12px; border-left: 1px solid var(--td-component-stroke); }
.workbench-metrics dt { color: var(--td-text-color-secondary); font-size: 11px; }
.workbench-metrics dd { margin: 4px 0 0; font-size: 13px; font-weight: 600; white-space: nowrap; }
.archived-alert { margin-top: 16px; }
.production-project-tabs { margin-top: 6px; }
.production-project-tabs :deep(.t-tabs__nav-wrap) { padding: 0; }
.production-project-tabs :deep(.t-tabs__content) { padding: 0; }
.tab-content { min-height: 320px; padding: 26px 0 0; }
.tab-state { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 6px; color: var(--td-text-color-secondary); border-bottom: 1px solid var(--td-component-stroke); text-align: center; }
.tab-state strong { color: var(--td-text-color-primary); font-size: 16px; }
.workbench-loading { padding-top: 32px; }
.workbench-state { min-height: 420px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; color: var(--td-text-color-secondary); text-align: center; }
.workbench-state > div { display: flex; flex-direction: column; gap: 4px; }
.workbench-state strong { color: var(--td-text-color-primary); font-size: 16px; }
.workbench-state--error { color: var(--td-error-color); }
@media (max-width: 760px) {
  .workbench-page { padding: 16px 16px 36px; }
  .workbench-header { grid-template-columns: 1fr; align-items: start; gap: 18px; }
  .workbench-metrics { width: 100%; grid-template-columns: 1fr 1fr 1.5fr; gap: 8px; }
  .workbench-metrics div { padding-left: 8px; }
  .workbench-metrics dd { white-space: normal; }
  .production-project-tabs :deep(.t-tabs__nav-item) { min-width: 84px; padding-inline: 12px; }
}
</style>
