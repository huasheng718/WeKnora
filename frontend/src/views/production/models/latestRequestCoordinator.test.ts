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

test('A to B route changes commit only the latest deferred response', async () => {
  const coordinator = createLatestRequestCoordinator()
  const requestA = deferred<string>()
  const requestB = deferred<string>()
  const commits: string[] = []
  const settled: string[] = []

  const loadA = coordinator.run(() => requestA.promise, {
    success: value => commits.push(value),
    settled: () => settled.push('A'),
  })
  const loadB = coordinator.run(() => requestB.promise, {
    success: value => commits.push(value),
    settled: () => settled.push('B'),
  })

  requestB.resolve('project-B')
  await loadB
  requestA.resolve('project-A')
  await loadA

  assert.deepEqual(commits, ['project-B'])
  assert.deepEqual(settled, ['B'])
})

test('stale failures do not replace the latest successful state', async () => {
  const coordinator = createLatestRequestCoordinator()
  const requestA = deferred<string>()
  const requestB = deferred<string>()
  const errors: string[] = []
  const commits: string[] = []

  const loadA = coordinator.run(() => requestA.promise, { error: cause => errors.push(String(cause)) })
  const loadB = coordinator.run(() => requestB.promise, { success: value => commits.push(value) })
  requestB.resolve('project-B')
  await loadB
  requestA.reject(new Error('project A failed'))
  await loadA

  assert.deepEqual(commits, ['project-B'])
  assert.deepEqual(errors, [])
})

test('invalidate suppresses pending success error and settled handlers', async () => {
  const coordinator = createLatestRequestCoordinator()
  const request = deferred<string>()
  const events: string[] = []
  const load = coordinator.run(() => request.promise, {
    success: () => events.push('success'),
    error: () => events.push('error'),
    settled: () => events.push('settled'),
  })

  coordinator.invalidate()
  request.resolve('project-A')
  await load

  assert.deepEqual(events, [])
})

test('an unmounted instance cannot overwrite a newer instance shared state', async t => {
  for (const outcome of ['success', 'error'] as const) {
    await t.test(outcome, async () => {
      const oldCoordinator = createLatestRequestCoordinator()
      const oldRequest = deferred<string>()
      const state = { active: 'project-A', loading: true, error: '' }
      const oldLoad = oldCoordinator.run(() => oldRequest.promise, {
        success: value => { state.active = value },
        error: cause => { state.error = String(cause) },
        settled: () => { state.loading = false },
      })

      oldCoordinator.invalidate()
      Object.assign(state, { active: 'project-list', loading: false, error: '' })

      const newCoordinator = createLatestRequestCoordinator()
      state.loading = true
      await newCoordinator.run(() => Promise.resolve('project-B'), {
        success: value => { state.active = value; state.error = '' },
        error: cause => { state.error = String(cause) },
        settled: () => { state.loading = false },
      })

      if (outcome === 'success') oldRequest.resolve('project-A')
      else oldRequest.reject(new Error('old project A failed'))
      await oldLoad

      assert.deepEqual(state, { active: 'project-B', loading: false, error: '' })
    })
  }
})
