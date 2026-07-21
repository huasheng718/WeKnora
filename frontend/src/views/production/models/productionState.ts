import type { ProductionReleaseTarget } from '@/api/production'

export interface ProductionReleaseTargetGroups {
  retryable: ProductionReleaseTarget[]
  inFlight: ProductionReleaseTarget[]
  ready: ProductionReleaseTarget[]
  active: ProductionReleaseTarget[]
}

export function normalizeProductionCollection<T extends { id: string }>(items?: readonly T[] | null): Record<string, T> {
  return Object.fromEntries((items ?? []).map(item => [item.id, item]))
}

export function groupReleaseTargets(targets?: readonly ProductionReleaseTarget[] | null): ProductionReleaseTargetGroups {
  const groups: ProductionReleaseTargetGroups = {
    retryable: [],
    inFlight: [],
    ready: [],
    active: [],
  }

  for (const target of targets ?? []) {
    if (target.status === 'failed') groups.retryable.push(target)
    if (target.status === 'building') groups.inFlight.push(target)
    if (target.status === 'ready') groups.ready.push(target)
    if (target.status === 'active') groups.active.push(target)
  }

  return groups
}
