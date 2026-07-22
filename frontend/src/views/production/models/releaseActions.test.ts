import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionReleaseTarget } from '@/api/production'
import {
  canConfirmProductionRelease,
  canPublishProductionRelease,
  releaseActions,
} from './releaseActions'

test('failed target exposes retry but not activate', () => {
  assert.deepEqual(releaseActions({ status: 'failed' } as ProductionReleaseTarget), ['retry'])
})

test('release target actions follow exact lifecycle states', () => {
  assert.deepEqual(releaseActions({ status: 'building' } as ProductionReleaseTarget), [])
  assert.deepEqual(releaseActions({ status: 'ready' } as ProductionReleaseTarget), ['activate'])
  assert.deepEqual(releaseActions({ status: 'active' } as ProductionReleaseTarget), ['rollback'])
  assert.deepEqual(releaseActions({ status: 'rolled_back', retention_until: '2099-01-01T00:00:00Z' } as ProductionReleaseTarget, new Date('2026-07-22T00:00:00Z')), ['rollback'])
  assert.deepEqual(releaseActions({ status: 'rolled_back', retention_until: '2026-07-21T00:00:00Z' } as ProductionReleaseTarget, new Date('2026-07-22T00:00:00Z')), [])
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
