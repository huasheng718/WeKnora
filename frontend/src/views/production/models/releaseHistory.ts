import type { ProductionPagedResponse } from '@/api/production'

export interface ReleaseHistoryRow {
  id: string
  created_at: string
}

export interface ReleaseHistoryPage<T extends ReleaseHistoryRow> {
  rows: T[]
  hasMore: boolean
}

export function releaseHistoryPage<T extends ReleaseHistoryRow>(responses: readonly ProductionPagedResponse<T>[]): ReleaseHistoryPage<T> {
  for (const response of responses) {
    if (!response.success) throw new Error(response.message || 'failed to load release history')
  }
  return {
    rows: responses.flatMap(response => response.data ?? []),
    hasMore: responses.some(response => response.has_more),
  }
}

export function appendReleaseHistory<T extends ReleaseHistoryRow>(current: readonly T[], next: readonly T[]): T[] {
  const rows = new Map<string, T>()
  for (const row of [...current, ...next]) rows.set(row.id, row)
  return [...rows.values()].sort((left, right) => Date.parse(right.created_at) - Date.parse(left.created_at))
}
