import type { TenantRole } from '@/api/tenant/members'
import type { ProductionProject, ProductionProjectRole } from '@/api/production'
import type { FormRule } from 'tdesign-vue-next'
import { productionAccess } from './productionAccess'
import type { ProjectSummary } from './projectSummary'

type ProjectAccessRow = Pick<ProductionProject, 'status'> & {
  current_user_roles?: readonly ProductionProjectRole[]
}

type ProjectSummaryRow = Pick<ProductionProject, 'updated_at' | 'summary'>

export type ProjectListViewState = 'loading' | 'error' | 'empty' | 'ready'
export type WorkbenchViewState = 'loading' | 'error' | 'missing' | 'ready'

export function canEditProductionProject(tenantRole: TenantRole | '', project: ProjectAccessRow): boolean {
  return project.status === 'active'
    && productionAccess(tenantRole, project.current_user_roles ?? []).edit
}

export function projectListViewState(input: {
  loading: boolean
  loaded: boolean
  error: string
  itemCount: number
}): ProjectListViewState {
  if (input.loading && !input.loaded) return 'loading'
  if (input.error) return 'error'
  if (input.itemCount === 0) return 'empty'
  return 'ready'
}

export function workbenchViewState(input: {
  loading: boolean
  loaded: boolean
  error: string
  hasProject: boolean
}): WorkbenchViewState {
  if (input.loading && !input.loaded) return 'loading'
  if (input.error) return 'error'
  if (!input.hasProject) return 'missing'
  return 'ready'
}

export function projectSummaryFromResponse(project: ProjectSummaryRow): ProjectSummary {
  const summary = project.summary
  return {
    documentCount: summary?.document_count ?? 0,
    sourceSetCount: summary?.source_set_count ?? 0,
    pendingReviews: summary?.pending_reviews ?? 0,
    failedTargets: summary?.failed_targets ?? 0,
    inFlightRuns: summary?.in_flight_runs ?? 0,
    latestActivity: summary?.latest_activity || project.updated_at,
  }
}

export function validateProductionProjectName(value: string): 'required' | 'too_long' | null {
  const length = Array.from((value ?? '').trim()).length
  if (length === 0) return 'required'
  if (length > 255) return 'too_long'
  return null
}

export function productionProjectNameRules(t: (key: string) => string): Record<string, FormRule[]> {
  return {
    name: [
      {
        validator: (value: string) => validateProductionProjectName(value) !== 'required',
        message: t('production.validation.nameRequired'),
        trigger: 'blur',
      },
      {
        validator: (value: string) => validateProductionProjectName(value) !== 'too_long',
        message: t('production.validation.nameTooLong'),
        trigger: 'change',
      },
    ],
  }
}

export function productionProjectLocation(projectId: string) {
  return { name: 'productionProject' as const, params: { projectId } }
}

export function productionDocumentLocation(documentId: string) {
  return { name: 'productionDocument' as const, params: { documentId } }
}
