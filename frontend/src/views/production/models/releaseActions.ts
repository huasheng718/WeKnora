import type {
  ProductionProjectRole,
  ProductionReleasePreflightTarget,
  ProductionReleaseTarget,
} from '@/api/production'
import type { TenantRole } from '@/api/tenant/members'

export type ProductionReleaseAction = 'activate' | 'retry' | 'rollback'

const TENANT_LEVEL: Record<TenantRole, number> = { viewer: 10, contributor: 20, admin: 30, owner: 40 }

export function releaseActions(target: Pick<ProductionReleaseTarget, 'status' | 'retention_until'>, now = new Date()): ProductionReleaseAction[] {
  if (target.status === 'failed') return ['retry']
  if (target.status === 'ready') return ['activate']
  if (target.status === 'active') return ['rollback']
  if (target.status === 'rolled_back' && target.retention_until && Date.parse(target.retention_until) > now.getTime()) return ['rollback']
  return []
}

export function canPublishProductionRelease(
  tenantRole: TenantRole | '',
  projectRoles: readonly ProductionProjectRole[],
): boolean {
  return !!tenantRole && TENANT_LEVEL[tenantRole] >= TENANT_LEVEL.contributor && projectRoles.includes('publisher')
}

export function canConfirmProductionRelease(
  selectedKnowledgeBaseIds: readonly string[],
  targets: readonly Pick<ProductionReleasePreflightTarget, 'knowledge_base_id' | 'ready'>[],
  confirmed: boolean,
): boolean {
  if (!confirmed || selectedKnowledgeBaseIds.length === 0) return false
  const targetById = new Map(targets.map(target => [target.knowledge_base_id, target]))
  return selectedKnowledgeBaseIds.every(id => targetById.get(id)?.ready === true)
}
