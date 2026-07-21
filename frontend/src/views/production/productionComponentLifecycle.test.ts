import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const workbenchSource = readFileSync(new URL('./ProductionProjectWorkbench.vue', import.meta.url), 'utf8')
const projectListSource = readFileSync(new URL('./ProductionProjectList.vue', import.meta.url), 'utf8')
const dialogSource = readFileSync(new URL('./components/ProductionProjectDialog.vue', import.meta.url), 'utf8')

test('workbench invalidates pending loads before unmount', () => {
  assert.match(workbenchSource, /onBeforeUnmount\(\(\) => loadCoordinator\.invalidate\(\)\)/)
})

test('project dialog does not force autofocus on narrow viewports', () => {
  assert.doesNotMatch(dialogSource, /\bautofocus\b/)
})

test('project list coordinates every load and invalidates pending loads before unmount', () => {
  assert.match(projectListSource, /createLatestRequestCoordinator\(\)/)
  assert.match(projectListSource, /onBeforeUnmount\(\(\) => loadCoordinator\.invalidate\(\)\)/)
  assert.doesNotMatch(projectListSource, /if \(loading\.value\) return/)
})
