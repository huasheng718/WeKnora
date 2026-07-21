import assert from 'node:assert/strict'
import test from 'node:test'

import {
  createProductionCommand,
  productionCommandConfig,
} from './idempotency'

test('a production command keeps its idempotency key across retries', () => {
  const command = createProductionCommand({ name: 'Baseline' }, () => 'request-1')

  assert.equal(command.idempotencyKey, 'request-1')
  assert.equal(productionCommandConfig(command).headers['Idempotency-Key'], 'request-1')
  assert.equal(productionCommandConfig(command).headers['Idempotency-Key'], 'request-1')
})

test('separate production commands do not share an idempotency key', () => {
  const keys = ['request-1', 'request-2']
  const nextKey = () => keys.shift()!

  const first = createProductionCommand({ name: 'Baseline' }, nextKey)
  const second = createProductionCommand({ name: 'Updated baseline' }, nextKey)

  assert.notEqual(first.idempotencyKey, second.idempotencyKey)
})
