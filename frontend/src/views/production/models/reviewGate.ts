import type {
  ProductionProjectRole,
  ProductionReview,
  ProductionReviewStep,
} from '@/api/production'
import type { TenantRole } from '@/api/tenant/members'
import { canEditProduction } from './productionAccess'

export type ReviewSubmitBlockReason = '' | 'blocking_annotations' | 'version_not_frozen' | 'historical_version' | 'review_pending' | 'author_role_required'

export interface ReviewSubmitGateInput {
  openBlocking: number
  frozen: boolean
  current?: boolean
  reviewStatus?: ProductionReview['status']
  tenantRole?: TenantRole | ''
  projectRoles?: readonly ProductionProjectRole[]
}

export function reviewSubmitGate(input: ReviewSubmitGateInput): { allowed: boolean; reason: ReviewSubmitBlockReason } {
  if (input.openBlocking > 0) return { allowed: false, reason: 'blocking_annotations' }
  if (!input.frozen) return { allowed: false, reason: 'version_not_frozen' }
  if (input.current === false) return { allowed: false, reason: 'historical_version' }
  if (input.reviewStatus === 'pending') return { allowed: false, reason: 'review_pending' }
  if (input.tenantRole !== undefined && !canEditProduction(input.tenantRole, input.projectRoles)) return { allowed: false, reason: 'author_role_required' }
  return { allowed: true, reason: '' }
}

const TENANT_LEVEL: Record<TenantRole, number> = { viewer: 10, contributor: 20, admin: 30, owner: 40 }

export function reviewStepActions(
  tenantRole: TenantRole | '',
  projectRoles: readonly ProductionProjectRole[],
  reviewStatus: ProductionReview['status'],
  step: Pick<ProductionReviewStep, 'required_role' | 'decision'>,
): Array<'approved' | 'changes_requested' | 'rejected'> {
  if (!tenantRole || TENANT_LEVEL[tenantRole] < TENANT_LEVEL.contributor || reviewStatus !== 'pending' || step.decision !== 'pending') return []
  return projectRoles.includes(step.required_role) ? ['approved', 'changes_requested', 'rejected'] : []
}

export function reviewTerminalActions(
  tenantRole: TenantRole | '',
  reviewStatus: ProductionReview['status'],
): Array<'reject' | 'cancel'> {
  if (!tenantRole || TENANT_LEVEL[tenantRole] < TENANT_LEVEL.admin || reviewStatus !== 'pending') return []
  return ['reject', 'cancel']
}
