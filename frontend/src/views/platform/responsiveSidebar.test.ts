import assert from 'node:assert/strict'
import test from 'node:test'
import { responsiveSidebarCollapsed } from './responsiveSidebar'

test('compact viewport collapses without changing the stored desktop preference', () => {
  assert.equal(responsiveSidebarCollapsed(true, false), true)
  assert.equal(responsiveSidebarCollapsed(true, true), true)
})

test('desktop viewport restores the persisted preference', () => {
  assert.equal(responsiveSidebarCollapsed(false, false), false)
  assert.equal(responsiveSidebarCollapsed(false, true), true)
})
