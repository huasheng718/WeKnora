import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import type {
  ProductionDocument,
  ProductionDocumentType,
  ProductionDocumentVersion,
  ProductionProject,
  ProductionReleaseTarget,
  ProductionReview,
  ProductionRun,
  ProductionSourceItem,
  ProductionSourceSet,
} from '@/api/production'
import { normalizeProductionCollection } from '@/views/production/models/productionState'

type ProductionEntity = { id: string }

function upsertProductionEntity<T extends ProductionEntity>(collection: Record<string, T>, entity: T): Record<string, T> {
  return { ...collection, [entity.id]: entity }
}

export const useProductionStore = defineStore('production', () => {
  const projectsById = ref<Record<string, ProductionProject>>({})
  const documentTypesById = ref<Record<string, ProductionDocumentType>>({})
  const sourceSetsById = ref<Record<string, ProductionSourceSet>>({})
  const sourceItemsById = ref<Record<string, ProductionSourceItem>>({})
  const documentsById = ref<Record<string, ProductionDocument>>({})
  const versionsById = ref<Record<string, ProductionDocumentVersion>>({})
  const runsById = ref<Record<string, ProductionRun>>({})
  const reviewsById = ref<Record<string, ProductionReview>>({})
  const releaseTargetsById = ref<Record<string, ProductionReleaseTarget>>({})

  const activeProjectId = ref<string | null>(null)
  const activeDocumentId = ref<string | null>(null)
  const activeVersionId = ref<string | null>(null)
  const loadingByKey = ref<Record<string, boolean>>({})
  const lastError = ref<string | null>(null)

  const activeProject = computed(() => activeProjectId.value ? projectsById.value[activeProjectId.value] ?? null : null)
  const activeDocument = computed(() => activeDocumentId.value ? documentsById.value[activeDocumentId.value] ?? null : null)
  const activeVersion = computed(() => activeVersionId.value ? versionsById.value[activeVersionId.value] ?? null : null)

  function replaceProjects(projects?: readonly ProductionProject[] | null) {
    projectsById.value = normalizeProductionCollection(projects)
  }

  function replaceDocumentTypes(documentTypes?: readonly ProductionDocumentType[] | null) {
    documentTypesById.value = normalizeProductionCollection(documentTypes)
  }

  function replaceVersions(versions?: readonly ProductionDocumentVersion[] | null) {
    versionsById.value = normalizeProductionCollection(versions)
  }

  function upsertProject(project: ProductionProject) { projectsById.value = upsertProductionEntity(projectsById.value, project) }
  function upsertSourceSet(sourceSet: ProductionSourceSet) { sourceSetsById.value = upsertProductionEntity(sourceSetsById.value, sourceSet) }
  function upsertSourceItem(sourceItem: ProductionSourceItem) { sourceItemsById.value = upsertProductionEntity(sourceItemsById.value, sourceItem) }
  function upsertDocument(document: ProductionDocument) { documentsById.value = upsertProductionEntity(documentsById.value, document) }
  function upsertVersion(version: ProductionDocumentVersion) { versionsById.value = upsertProductionEntity(versionsById.value, version) }
  function upsertRun(run: ProductionRun) { runsById.value = upsertProductionEntity(runsById.value, run) }
  function upsertReview(review: ProductionReview) { reviewsById.value = upsertProductionEntity(reviewsById.value, review) }
  function upsertReleaseTarget(target: ProductionReleaseTarget) { releaseTargetsById.value = upsertProductionEntity(releaseTargetsById.value, target) }

  function setLoading(key: string, loading: boolean) {
    loadingByKey.value = { ...loadingByKey.value, [key]: loading }
  }

  function setError(error: unknown) {
    lastError.value = error instanceof Error ? error.message : String(error)
  }

  function clearError() {
    lastError.value = null
  }

  return {
    projectsById,
    documentTypesById,
    sourceSetsById,
    sourceItemsById,
    documentsById,
    versionsById,
    runsById,
    reviewsById,
    releaseTargetsById,
    activeProjectId,
    activeDocumentId,
    activeVersionId,
    loadingByKey,
    lastError,
    activeProject,
    activeDocument,
    activeVersion,
    replaceProjects,
    replaceDocumentTypes,
    replaceVersions,
    upsertProject,
    upsertSourceSet,
    upsertSourceItem,
    upsertDocument,
    upsertVersion,
    upsertRun,
    upsertReview,
    upsertReleaseTarget,
    setLoading,
    setError,
    clearError,
  }
})
