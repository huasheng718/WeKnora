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
