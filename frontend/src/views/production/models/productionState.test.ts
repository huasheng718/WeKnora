import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionReleaseTarget } from '@/api/production'
import { groupReleaseTargets } from './productionState'

test('groups release targets by actionable state', () => {
  const result = groupReleaseTargets([
    { id: 'a', status: 'active' },
    { id: 'b', status: 'failed' },
    { id: 'c', status: 'building' },
  ] as ProductionReleaseTarget[])

  assert.deepEqual(result.retryable.map(item => item.id), ['b'])
  assert.deepEqual(result.inFlight.map(item => item.id), ['c'])
})

test('normalizes missing production collections to empty arrays', () => {
  const result = groupReleaseTargets(undefined)

  assert.deepEqual(result.retryable, [])
  assert.deepEqual(result.inFlight, [])
  assert.deepEqual(result.ready, [])
  assert.deepEqual(result.active, [])
})
