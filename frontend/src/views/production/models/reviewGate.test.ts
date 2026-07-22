import assert from 'node:assert/strict'
import test from 'node:test'

import {
  reviewStepActions,
  reviewSubmitGate,
  reviewTerminalActions,
} from './reviewGate'

test('blocking annotations disable submit with explicit reason', () => {
  assert.deepEqual(reviewSubmitGate({ openBlocking: 2, frozen: false }), {
    allowed: false,
    reason: 'blocking_annotations',
  })
})

test('review submit requires a frozen current version without a pending review', () => {
  assert.deepEqual(reviewSubmitGate({ openBlocking: 0, frozen: false }), { allowed: false, reason: 'version_not_frozen' })
  assert.deepEqual(reviewSubmitGate({ openBlocking: 0, frozen: true, current: false }), { allowed: false, reason: 'historical_version' })
  assert.deepEqual(reviewSubmitGate({ openBlocking: 0, frozen: true, current: true, reviewStatus: 'pending' }), { allowed: false, reason: 'review_pending' })
  assert.deepEqual(reviewSubmitGate({ openBlocking: 0, frozen: true, current: true }), { allowed: true, reason: '' })
})

test('review submit requires the contributor-floor authoring role enforced by the server', () => {
  assert.deepEqual(reviewSubmitGate({
    openBlocking: 0,
    frozen: true,
    current: true,
    tenantRole: 'viewer',
    projectRoles: ['author'],
  }), { allowed: false, reason: 'author_role_required' })
  assert.deepEqual(reviewSubmitGate({
    openBlocking: 0,
    frozen: true,
    current: true,
    tenantRole: 'contributor',
    projectRoles: ['business_reviewer'],
  }), { allowed: false, reason: 'author_role_required' })
  assert.deepEqual(reviewSubmitGate({
    openBlocking: 0,
    frozen: true,
    current: true,
    tenantRole: 'contributor',
    projectRoles: ['author'],
  }), { allowed: true, reason: '' })
})

test('only the exact professional project role can decide a pending step', () => {
  const step = { required_role: 'engineering_reviewer', decision: 'pending' } as const
  assert.deepEqual(reviewStepActions('contributor', ['engineering_reviewer'], 'pending', step), ['approved', 'changes_requested', 'rejected'])
  assert.deepEqual(reviewStepActions('contributor', ['business_reviewer'], 'pending', step), [])
  assert.deepEqual(reviewStepActions('viewer', ['engineering_reviewer'], 'pending', step), [])
  assert.deepEqual(reviewStepActions('contributor', ['engineering_reviewer'], 'approved', step), [])
})

test('terminal review actions follow the backend tenant admin floor', () => {
  assert.deepEqual(reviewTerminalActions('admin', 'pending'), ['reject', 'cancel'])
  assert.deepEqual(reviewTerminalActions('owner', 'pending'), ['reject', 'cancel'])
  assert.deepEqual(reviewTerminalActions('contributor', 'pending'), [])
  assert.deepEqual(reviewTerminalActions('admin', 'approved'), [])
})
