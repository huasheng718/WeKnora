import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionProjectRole } from '@/api/production'
import { productionAccess } from './productionAccess'

test('viewer can view but cannot author or publish', () => {
  const access = productionAccess('viewer', ['observer'])

  assert.equal(access.view, true)
  assert.equal(access.edit, false)
  assert.equal(access.publish, false)
})

test('publisher role requires contributor tenant floor', () => {
  assert.equal(productionAccess('viewer', ['publisher']).publish, false)
  assert.equal(productionAccess('contributor', ['publisher']).publish, true)
})

test('authoring requires contributor floor and an authoring project role', () => {
  for (const role of ['project_owner', 'author'] satisfies ProductionProjectRole[]) {
    assert.equal(productionAccess('viewer', [role]).edit, false, `viewer with ${role}`)
    assert.equal(productionAccess('contributor', [role]).edit, true, `contributor with ${role}`)
  }

  for (const role of [
    'business_reviewer',
    'engineering_reviewer',
    'knowledge_admin',
    'compliance_reviewer',
    'publisher',
    'observer',
  ] satisfies ProductionProjectRole[]) {
    assert.equal(productionAccess('contributor', [role]).edit, false, role)
  }
})

test('publishing requires the exact publisher project role', () => {
  const nonPublishers = [
    'project_owner',
    'author',
    'business_reviewer',
    'engineering_reviewer',
    'knowledge_admin',
    'compliance_reviewer',
    'observer',
  ] satisfies ProductionProjectRole[]

  for (const role of nonPublishers) {
    assert.equal(productionAccess('owner', [role]).publish, false, role)
  }
  assert.equal(productionAccess('admin', ['publisher']).publish, true)
  assert.equal(productionAccess('owner', ['publisher']).publish, true)
})

test('tenant admin and owner do not substitute for project roles', () => {
  for (const tenantRole of ['admin', 'owner'] as const) {
    const access = productionAccess(tenantRole, [])
    assert.equal(access.view, true, `${tenantRole} view`)
    assert.equal(access.edit, false, `${tenantRole} edit`)
    assert.equal(access.publish, false, `${tenantRole} publish`)
  }
})

test('production root visibility follows the viewer tenant floor', () => {
  for (const tenantRole of ['viewer', 'contributor', 'admin', 'owner'] as const) {
    assert.equal(productionAccess(tenantRole).view, true, tenantRole)
  }

  assert.deepEqual(productionAccess('', ['project_owner', 'publisher']), {
    view: false,
    edit: false,
    publish: false,
  })
})
