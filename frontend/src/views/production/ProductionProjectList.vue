<template>
  <main class="production-page">
    <header class="page-header">
      <div>
        <p class="page-kicker">{{ t('production.workspace') }}</p>
        <h1>{{ t('production.projects.title') }}</h1>
        <p>{{ t('production.projects.description') }}</p>
      </div>
      <t-tooltip :content="canCreate ? t('production.projects.create') : t('production.permissions.createDenied')">
        <span>
          <t-button :disabled="!canCreate || loading" @click="dialogVisible = true">
            <template #icon><t-icon name="add" /></template>
            {{ t('production.projects.create') }}
          </t-button>
        </span>
      </t-tooltip>
    </header>

    <div class="list-toolbar" aria-live="polite">
      <span>{{ t('production.projects.count', { count: projects.length }) }}</span>
      <t-button variant="text" size="small" :loading="loading" @click="loadProjects">
        <template #icon><t-icon name="refresh" /></template>
        {{ t('production.actions.refresh') }}
      </t-button>
    </div>

    <div v-if="viewState === 'loading'" class="project-grid" aria-live="polite">
      <div v-for="card in 6" :key="card" class="project-card project-card--skeleton">
        <t-skeleton animation="gradient" :row-col="[{ width: '68%', height: '22px' }, { width: '100%', height: '42px' }, { width: '88%', height: '30px' }]" />
      </div>
    </div>

    <div v-else-if="viewState === 'error'" class="page-state page-state--error" role="alert">
      <t-icon name="error-circle" size="28px" />
      <div><strong>{{ t('production.errors.loadProjectsTitle') }}</strong><span>{{ error }}</span></div>
      <t-button variant="outline" size="small" @click="loadProjects">{{ t('production.actions.retry') }}</t-button>
    </div>

    <div v-else-if="viewState === 'empty'" class="page-state">
      <t-icon name="folder-open" size="34px" />
      <div>
        <strong>{{ t('production.projects.emptyTitle') }}</strong>
        <span>{{ canCreate ? t('production.projects.emptyEditable') : t('production.projects.emptyReadonly') }}</span>
      </div>
      <t-button v-if="canCreate" size="small" @click="dialogVisible = true">
        <template #icon><t-icon name="add" /></template>
        {{ t('production.projects.create') }}
      </t-button>
    </div>

    <section v-else class="project-grid" :aria-label="t('production.projects.title')">
      <article
        v-for="project in projects"
        :key="project.id"
        class="project-card"
        :class="{ 'project-card--archived': project.status === 'archived' }"
      >
        <div class="project-card__topline">
          <span class="project-card__icon"><t-icon name="folder" /></span>
          <t-tag size="small" variant="light" :theme="project.status === 'active' ? 'success' : 'default'">
            {{ t(`production.projectStatus.${project.status}`) }}
          </t-tag>
        </div>
        <div class="project-card__copy">
          <h2>{{ project.name }}</h2>
          <p>{{ project.description || t('production.projects.noDescription') }}</p>
        </div>
        <dl class="project-metrics">
          <div><dt>{{ t('production.metrics.documents') }}</dt><dd>{{ summary(project).documentCount }}</dd></div>
          <div><dt>{{ t('production.metrics.sourceSets') }}</dt><dd>{{ summary(project).sourceSetCount }}</dd></div>
          <div><dt>{{ t('production.metrics.activity') }}</dt><dd>{{ formatDate(summary(project).latestActivity) }}</dd></div>
        </dl>
        <div class="project-card__footer">
          <span v-if="summary(project).inFlightRuns > 0" class="activity-indicator">
            <span aria-hidden="true" />{{ t('production.metrics.inFlight', { count: summary(project).inFlightRuns }) }}
          </span>
          <span v-else />
          <t-button variant="text" size="small" @click="openProject(project.id)">
            {{ t('production.actions.open') }}
            <template #suffix><t-icon name="chevron-right" /></template>
          </t-button>
        </div>
      </article>
    </section>

    <ProductionProjectDialog v-model:visible="dialogVisible" :can-create="canCreate" @created="onProjectCreated" />
  </main>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { TenantRole } from '@/api/tenant/members'
import {
  listProductionProjects,
  type ProductionProject,
} from '@/api/production'
import { useAuthStore } from '@/stores/auth'
import { useProductionStore } from '@/stores/production'
import { productionAccess } from './models/productionAccess'
import { createLatestRequestCoordinator } from './models/latestRequestCoordinator'
import {
  productionProjectLocation,
  projectListViewState,
  projectSummaryFromResponse,
} from './models/productionViewModel'
import ProductionProjectDialog from './components/ProductionProjectDialog.vue'

const { t, locale } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const store = useProductionStore()
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const dialogVisible = ref(false)
const loadCoordinator = createLatestRequestCoordinator()

const projects = computed(() => Object.values(store.projectsById).sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at)))
const canCreate = computed(() => productionAccess(auth.currentTenantRole as TenantRole | '', ['project_owner']).edit)
const viewState = computed(() => projectListViewState({
  loading: loading.value,
  loaded: loaded.value,
  error: error.value,
  itemCount: projects.value.length,
}))

function summary(project: ProductionProject) {
  return projectSummaryFromResponse(project)
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium' }).format(new Date(value))
}

function openProject(projectId: string) {
  router.push(productionProjectLocation(projectId))
}

function onProjectCreated(project: ProductionProject) {
  store.upsertProject(project)
  openProject(project.id)
}

async function loadProjects() {
  loading.value = true
  error.value = ''
  await loadCoordinator.run(async () => {
    const projectResponse = await listProductionProjects()
    if (!projectResponse.success) throw new Error(projectResponse.message || t('production.errors.loadProjects'))
    return projectResponse.data ?? []
  }, {
    success: projects => {
      store.replaceProjects(projects)
      loaded.value = true
    },
    error: cause => {
      error.value = cause instanceof Error ? cause.message : t('production.errors.loadProjects')
    },
    settled: () => {
      loading.value = false
    },
  })
}

onMounted(loadProjects)
onBeforeUnmount(() => loadCoordinator.invalidate())
</script>

<style scoped>
.production-page { width: min(1240px, 100%); margin: 0 auto; padding: 28px 32px 48px; box-sizing: border-box; color: var(--td-text-color-primary); }
.page-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 32px; padding-bottom: 24px; border-bottom: 1px solid var(--td-component-stroke); }
.page-kicker { margin: 0 0 5px !important; color: var(--td-brand-color) !important; font-size: 12px !important; font-weight: 600; text-transform: uppercase; }
.page-header h1 { margin: 0; font-size: 26px; line-height: 34px; letter-spacing: 0; }
.page-header p { max-width: 680px; margin: 6px 0 0; color: var(--td-text-color-secondary); font-size: 14px; line-height: 22px; }
.list-toolbar { min-height: 48px; display: flex; align-items: center; justify-content: space-between; color: var(--td-text-color-secondary); font-size: 13px; }
.project-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 290px), 1fr)); gap: 14px; }
.project-card { min-height: 242px; display: flex; flex-direction: column; padding: 18px; box-sizing: border-box; border: 1px solid var(--td-component-stroke); border-radius: var(--td-radius-medium); background: var(--td-bg-color-container); }
.project-card:hover { border-color: var(--td-brand-color-4); }
.project-card--archived { opacity: .62; background: var(--td-bg-color-secondarycontainer); }
.project-card--skeleton { justify-content: center; }
.project-card__topline, .project-card__footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.project-card__icon { width: 34px; height: 34px; display: grid; place-items: center; border-radius: var(--td-radius-medium); background: var(--td-bg-color-secondarycontainer); color: var(--td-brand-color); }
.project-card__copy { min-height: 82px; margin-top: 15px; }
.project-card__copy h2 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 17px; line-height: 25px; letter-spacing: 0; }
.project-card__copy p { display: -webkit-box; margin: 5px 0 0; overflow: hidden; -webkit-box-orient: vertical; -webkit-line-clamp: 2; color: var(--td-text-color-secondary); font-size: 13px; line-height: 20px; }
.project-metrics { display: grid; grid-template-columns: 72px 78px minmax(0, 1fr); gap: 10px; margin: 14px 0; padding: 12px 0; border-block: 1px solid var(--td-component-stroke); }
.project-metrics div { min-width: 0; }
.project-metrics dt { color: var(--td-text-color-secondary); font-size: 11px; line-height: 18px; }
.project-metrics dd { margin: 2px 0 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 600; }
.project-card__footer { margin-top: auto; min-height: 28px; }
.activity-indicator { display: inline-flex; align-items: center; gap: 6px; color: var(--td-text-color-secondary); font-size: 12px; }
.activity-indicator > span { width: 7px; height: 7px; border-radius: 50%; background: var(--td-success-color); }
.page-state { min-height: 340px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; color: var(--td-text-color-secondary); border-block: 1px solid var(--td-component-stroke); text-align: center; }
.page-state > div { display: flex; flex-direction: column; gap: 4px; }
.page-state strong { color: var(--td-text-color-primary); font-size: 16px; }
.page-state--error { min-height: 180px; color: var(--td-error-color); }
@media (max-width: 720px) {
  .production-page { padding: 20px 16px 36px; }
  .page-header { flex-direction: column; gap: 18px; }
  .page-header > span, .page-header :deep(.t-button) { width: 100%; }
  .project-card { min-height: 228px; }
}
</style>
