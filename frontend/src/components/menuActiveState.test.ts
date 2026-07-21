import assert from 'node:assert/strict'
import test from 'node:test'

import { isMenuPathActive } from './menuActiveState'

test('knowledge production routes activate only the knowledge production menu', () => {
  const productionRoutes = ['productionProjects', 'productionProject', 'productionDocument']
  const otherMenuPaths = ['knowledge-bases', 'agents', 'organizations', 'creatChat', 'settings']

  for (const routeName of productionRoutes) {
    assert.equal(isMenuPathActive('knowledge-production', routeName), true, routeName)
    for (const itemPath of otherMenuPaths) {
      assert.equal(isMenuPathActive(itemPath, routeName), false, `${routeName} -> ${itemPath}`)
    }
  }
})

test('existing named route groups keep their menu active behavior', () => {
  const cases = [
    ['knowledge-bases', 'knowledgeBaseList'],
    ['knowledge-bases', 'knowledgeBaseDetail'],
    ['knowledge-bases', 'knowledgeBaseSettings'],
    ['agents', 'agentList'],
    ['organizations', 'organizationList'],
    ['creatChat', 'kbCreatChat'],
    ['creatChat', 'globalCreatChat'],
    ['settings', 'settings'],
  ] as const

  for (const [itemPath, routeName] of cases) {
    assert.equal(isMenuPathActive(itemPath, routeName), true, `${routeName} -> ${itemPath}`)
  }
})

test('unknown menu paths retain exact route-name fallback behavior', () => {
  assert.equal(isMenuPathActive('custom-route', 'custom-route'), true)
  assert.equal(isMenuPathActive('custom-route', 'another-route'), false)
  assert.equal(isMenuPathActive('custom-route', undefined), false)
})
