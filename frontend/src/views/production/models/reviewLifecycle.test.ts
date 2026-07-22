import assert from 'node:assert/strict'
import test from 'node:test'

import { commandForReviewVersion } from './reviewLifecycle'

test('a failed review command is not reused when the selected version changes', () => {
  const first = commandForReviewVersion('version-a', null, () => ({ key: 'command-a' }))
  const retry = commandForReviewVersion('version-a', first, () => ({ key: 'command-b' }))
  const changed = commandForReviewVersion('version-b', first, () => ({ key: 'command-c' }))

  assert.equal(retry.command.key, 'command-a')
  assert.equal(changed.command.key, 'command-c')
  assert.equal(changed.versionId, 'version-b')
})
