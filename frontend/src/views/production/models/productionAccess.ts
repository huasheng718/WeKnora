import type { ProductionProjectRole } from '@/api/production'
import type { TenantRole } from '@/api/tenant/members'

export interface ProductionAccess {
  view: boolean
  edit: boolean
  publish: boolean
}

type ProductionTenantRole = TenantRole | '' | null | undefined

const TENANT_ROLE_LEVEL: Record<TenantRole, number> = {
  viewer: 10,
  contributor: 20,
  admin: 30,
  owner: 40,
}

const AUTHORING_ROLES = new Set<ProductionProjectRole>(['project_owner', 'author'])

function hasTenantFloor(role: ProductionTenantRole, floor: TenantRole): boolean {
  if (!role) return false
  return TENANT_ROLE_LEVEL[role] >= TENANT_ROLE_LEVEL[floor]
}

export function canViewProduction(tenantRole: ProductionTenantRole): boolean {
  return hasTenantFloor(tenantRole, 'viewer')
}

export function canEditProduction(
  tenantRole: ProductionTenantRole,
  projectRoles: readonly ProductionProjectRole[] = [],
): boolean {
  return hasTenantFloor(tenantRole, 'contributor')
    && projectRoles.some(role => AUTHORING_ROLES.has(role))
}

export function canPublishProduction(
  tenantRole: ProductionTenantRole,
  projectRoles: readonly ProductionProjectRole[] = [],
): boolean {
  return hasTenantFloor(tenantRole, 'contributor')
    && projectRoles.includes('publisher')
}

export function productionAccess(
  tenantRole: ProductionTenantRole,
  projectRoles: readonly ProductionProjectRole[] = [],
): ProductionAccess {
  return {
    view: canViewProduction(tenantRole),
    edit: canEditProduction(tenantRole, projectRoles),
    publish: canPublishProduction(tenantRole, projectRoles),
  }
}
