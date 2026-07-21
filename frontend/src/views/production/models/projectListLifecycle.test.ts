import assert from 'node:assert/strict'
import test from 'node:test'

import { createLatestRequestCoordinator } from './latestRequestCoordinator'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

test('an unmounted project list cannot overwrite a newer workbench shared state', async t => {
  for (const outcome of ['success', 'error'] as const) {
    await t.test(outcome, async () => {
      const listCoordinator = createLatestRequestCoordinator()
      const listRequest = deferred<string[]>()
      const state = { projects: ['old-list'], active: '', loading: true, error: '' }
      const listLoad = listCoordinator.run(() => listRequest.promise, {
        success: projects => { state.projects = projects; state.active = '' },
        error: cause => { state.error = String(cause) },
        settled: () => { state.loading = false },
      })

      listCoordinator.invalidate()
      Object.assign(state, { projects: ['project-B'], active: 'project-B', loading: false, error: '' })

      if (outcome === 'success') listRequest.resolve(['stale-list-project'])
      else listRequest.reject(new Error('stale list failed'))
      await listLoad

      assert.deepEqual(state, { projects: ['project-B'], active: 'project-B', loading: false, error: '' })
    })
  }
})

test('multiple project list refreshes commit only the latest response', async () => {
  const coordinator = createLatestRequestCoordinator()
  const first = deferred<string[]>()
  const second = deferred<string[]>()
  const commits: string[][] = []

  const firstLoad = coordinator.run(() => first.promise, { success: rows => commits.push(rows) })
  const secondLoad = coordinator.run(() => second.promise, { success: rows => commits.push(rows) })
  second.resolve(['latest'])
  await secondLoad
  first.resolve(['stale'])
  await firstLoad

  assert.deepEqual(commits, [['latest']])
})
