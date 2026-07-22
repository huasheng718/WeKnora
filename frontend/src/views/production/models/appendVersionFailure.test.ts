import assert from 'node:assert/strict'
import test from 'node:test'

import { resolveAppendVersionFailure } from './appendVersionFailure'

test('a flattened stale-parent 409 exposes conflict recovery and discards its append command', () => {
  const command = { idempotencyKey: 'stale-save-command' }

  assert.deepEqual(
    resolveAppendVersionFailure({ status: 409, message: 'parent version is stale' }, command),
    { conflict: true, command: null },
  )
})

test('a non-conflict save failure preserves its append command for idempotent retry', () => {
  const command = { idempotencyKey: 'retryable-save-command' }

  assert.deepEqual(
    resolveAppendVersionFailure({ status: 503, message: 'service unavailable' }, command),
    { conflict: false, command },
  )
})
