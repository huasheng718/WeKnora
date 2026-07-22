import assert from 'node:assert/strict'
import test from 'node:test'

import { createScopedMutationCoordinator } from './scopedMutationCoordinator'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

test('a deferred A annotation cannot overwrite B after a document change', async t => {
  for (const outcome of ['success', 'failure'] as const) {
    await t.test(outcome, async () => {
      const coordinator = createScopedMutationCoordinator()
      const requestA = deferred<string>()
      const requestB = deferred<string>()
      const state = {
        documentId: 'document-A',
        annotations: [] as string[],
        draft: 'draft A',
        error: '',
        submitting: true,
      }

      const run = async (documentId: string, request: Promise<string>) => {
        const mutation = coordinator.start(documentId)
        try {
          const annotation = await request
          if (!coordinator.isCurrent(mutation, state.documentId)) return
          state.annotations.unshift(annotation)
          state.draft = ''
        } catch (cause) {
          if (coordinator.isCurrent(mutation, state.documentId)) state.error = String(cause)
        } finally {
          if (coordinator.isCurrent(mutation, state.documentId)) state.submitting = false
        }
      }

      const pendingA = run('document-A', requestA.promise)
      coordinator.invalidate()
      Object.assign(state, { documentId: 'document-B', draft: 'draft B', submitting: false })

      state.submitting = true
      const pendingB = run('document-B', requestB.promise)

      if (outcome === 'success') requestA.resolve('annotation-A')
      else requestA.reject(new Error('annotation A failed'))
      await pendingA

      assert.deepEqual(state, {
        documentId: 'document-B',
        annotations: [],
        draft: 'draft B',
        error: '',
        submitting: true,
      })

      requestB.resolve('annotation-B')
      await pendingB

      assert.deepEqual(state, {
        documentId: 'document-B',
        annotations: ['annotation-B'],
        draft: '',
        error: '',
        submitting: false,
      })
    })
  }
})

test('an unmounted workbench ignores a deferred annotation result', async t => {
  for (const outcome of ['success', 'failure'] as const) {
    await t.test(outcome, async () => {
      const coordinator = createScopedMutationCoordinator()
      const request = deferred<string>()
      const mutation = coordinator.start('document-A')
      const events: string[] = []
      const pending = request.promise.then(
        () => { if (coordinator.isCurrent(mutation, 'document-A')) events.push('success') },
        () => { if (coordinator.isCurrent(mutation, 'document-A')) events.push('error') },
      ).finally(() => {
        if (coordinator.isCurrent(mutation, 'document-A')) events.push('settled')
      })

      coordinator.invalidate()
      if (outcome === 'success') request.resolve('annotation-A')
      else request.reject(new Error('annotation A failed'))
      await pending

      assert.deepEqual(events, [])
    })
  }
})

test('a stale release confirm cannot alter a reopened dialog', async () => {
  const coordinator = createScopedMutationCoordinator()
  const request = deferred<void>()
  const state = { scope: 'open:document-A:version-A', submitting: true, error: '' }
  const mutation = coordinator.start(state.scope)
  const pending = request.promise.catch(cause => {
    if (coordinator.isCurrent(mutation, state.scope)) state.error = String(cause)
  }).finally(() => {
    if (coordinator.isCurrent(mutation, state.scope)) state.submitting = false
  })

  coordinator.invalidate()
  Object.assign(state, { scope: 'open:document-A:version-B', submitting: false, error: '' })
  request.reject(new Error('stale confirm failed'))
  await pending

  assert.deepEqual(state, { scope: 'open:document-A:version-B', submitting: false, error: '' })
})
