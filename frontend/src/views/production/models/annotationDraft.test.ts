import assert from 'node:assert/strict'
import test from 'node:test'

import { buildAnnotationSubmission, completeAnnotationDraft } from './annotationDraft'

test('failed annotation submission retains draft and reuses the same command', () => {
  const draft = { body: 'Keep this issue', severity: 'warning' as const }
  const anchor = {
    versionId: 'version-1', blockId: 'block-1', logicalBlockId: 'logical-1',
  }
  const first = buildAnnotationSubmission(draft, anchor, null, () => 'annotation-command')
  const retry = buildAnnotationSubmission(draft, anchor, first, () => 'different-command')

  assert.equal(first.command.idempotencyKey, 'annotation-command')
  assert.equal(retry.command.idempotencyKey, 'annotation-command')
  assert.deepEqual(draft, { body: 'Keep this issue', severity: 'warning' })
})

test('annotation draft clears only after confirmed success', () => {
  const draft = { body: 'Resolved by server', severity: 'blocking' as const }
  assert.deepEqual(completeAnnotationDraft(draft), { body: '', severity: 'blocking' })
  assert.deepEqual(draft, { body: 'Resolved by server', severity: 'blocking' })
})
