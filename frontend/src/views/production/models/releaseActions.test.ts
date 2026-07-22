import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionReleaseTarget } from '@/api/production'
import {
  canConfirmProductionRelease,
  canPublishProductionRelease,
  releaseActions,
} from './releaseActions'
import { appendReleaseHistory, releaseHistoryPage } from './releaseHistory'

test('failed target exposes retry but not activate', () => {
  assert.deepEqual(releaseActions({ status: 'failed' } as ProductionReleaseTarget), ['retry'])
})

test('release target actions follow exact lifecycle states', () => {
  assert.deepEqual(releaseActions({ status: 'building' } as ProductionReleaseTarget), [])
  assert.deepEqual(releaseActions({ status: 'ready' } as ProductionReleaseTarget), ['activate'])
  assert.deepEqual(releaseActions({ status: 'active' } as ProductionReleaseTarget), [])
  assert.deepEqual(releaseActions({ status: 'rolled_back', retention_until: '2099-01-01T00:00:00Z' } as ProductionReleaseTarget, new Date('2026-07-22T00:00:00Z')), ['rollback'])
  assert.deepEqual(releaseActions({ status: 'rolled_back', retention_until: '2026-07-21T00:00:00Z' } as ProductionReleaseTarget, new Date('2026-07-22T00:00:00Z')), [])
})

test('release history rejects failed pages and globally orders appended rows', () => {
  assert.throws(() => releaseHistoryPage([{ success: false, message: 'denied', page: 1, page_size: 20, total: 0, has_more: false }]), /denied/)
  const first = releaseHistoryPage([{ success: true, data: [{ id: 'older', created_at: '2026-07-01T00:00:00Z' }], page: 1, page_size: 20, total: 2, has_more: true }])
  const second = releaseHistoryPage([{ success: true, data: [{ id: 'newer', created_at: '2026-07-02T00:00:00Z' }], page: 2, page_size: 20, total: 2, has_more: false }])
  assert.deepEqual(appendReleaseHistory(first.rows, second.rows).map(row => row.id), ['newer', 'older'])
})

test('publication actions require contributor floor and exact publisher project role', () => {
  assert.equal(canPublishProductionRelease('contributor', ['publisher']), true)
  assert.equal(canPublishProductionRelease('admin', ['publisher']), true)
  assert.equal(canPublishProductionRelease('viewer', ['publisher']), false)
  assert.equal(canPublishProductionRelease('owner', ['project_owner']), false)
})

test('release confirmation requires confirmation and successful preflight for every selected target', () => {
  const targets = [
    { knowledge_base_id: 'kb-1', ready: true },
    { knowledge_base_id: 'kb-2', ready: false },
  ]
  assert.equal(canConfirmProductionRelease(['kb-1'], targets, true), true)
  assert.equal(canConfirmProductionRelease(['kb-1'], targets, false), false)
  assert.equal(canConfirmProductionRelease(['kb-1', 'kb-2'], targets, true), false)
  assert.equal(canConfirmProductionRelease([], targets, true), false)
})
