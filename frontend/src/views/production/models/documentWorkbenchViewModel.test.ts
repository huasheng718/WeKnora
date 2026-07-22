import assert from 'node:assert/strict'
import test from 'node:test'

import { documentWorkbenchLayout } from './documentWorkbenchViewModel'

test('workbench uses rails on desktop and drawers below 1100px without shrinking the editor', () => {
  assert.deepEqual(documentWorkbenchLayout(1280), { mode: 'rails', editorMinWidth: 520 })
  assert.deepEqual(documentWorkbenchLayout(1099), { mode: 'drawers', editorMinWidth: 320 })
  assert.deepEqual(documentWorkbenchLayout(375), { mode: 'drawers', editorMinWidth: 320 })
})
