import assert from 'node:assert/strict'
import test from 'node:test'

const values = new Map<string, string>()
globalThis.window = {
  __RUNTIME_CONFIG__: {},
  location: { pathname: '/', href: '' },
} as unknown as Window & typeof globalThis
globalThis.document = {
  addEventListener() {},
  removeEventListener() {},
  createElement: () => ({ content: {}, style: {}, setAttribute() {} }),
} as unknown as Document
globalThis.localStorage = {
  getItem: key => values.get(key) ?? null,
  setItem: (key, value) => values.set(key, value),
  removeItem: key => values.delete(key),
  clear: () => values.clear(),
  key: index => [...values.keys()][index] ?? null,
  get length() { return values.size },
} as Storage

const axios = (await import('axios')).default
const requests: Array<Record<string, any>> = []
axios.defaults.adapter = async config => {
  requests.push(config as Record<string, any>)
  return { data: { success: true }, status: 200, statusText: 'OK', headers: {}, config }
}

const production = await import('./index')
const { createProductionCommand } = await import('./idempotency')

type ReviewStepCommand = Parameters<typeof production.decideProductionReviewStep>[2]
type ReviewStepInput = ReviewStepCommand extends import('./idempotency').ProductionCommand<infer T> ? T : never
const supportedReviewStepDecisions: ReviewStepInput['decision'][] = ['approved', 'rejected', 'changes_requested']
// @ts-expect-error The backend does not accept the legacy imperative spelling.
const legacyApproveDecision: ReviewStepInput['decision'] = 'approve'
// @ts-expect-error The backend does not accept the legacy imperative spelling.
const legacyRejectDecision: ReviewStepInput['decision'] = 'reject'
void supportedReviewStepDecisions
void legacyApproveDecision
void legacyRejectDecision

function bodyOf(request: Record<string, any>) {
  return typeof request.data === 'string' ? JSON.parse(request.data) : request.data
}

test('production review clients match annotation and terminal review HTTP contracts', async () => {
  requests.length = 0
  const command = <T>(payload: T) => createProductionCommand(payload, () => 'review-command')

  await production.createProductionAnnotation('document-1', command({
    version_id: 'version-1',
    block_id: 'block-1',
    annotation_type: 'comment',
    severity: 'info',
    anchor: { start: 0 },
    body: 'Check this claim',
  }))
  await production.listProductionAnnotations('document-1', {
    version_id: 'version-1', status: 'open', annotation_type: 'comment', severity: 'info', page: 2, page_size: 25,
  })
  await production.updateProductionAnnotationStatus('annotation-1', command({ status: 'resolved' }))
  await production.decideProductionReviewStep('review-1', 'step-1', command({
    decision: 'approved',
    comment: 'Ready to publish',
  }))
  await production.rejectProductionReview('review-1', command({ reason: 'Policy mismatch' }))
  await production.cancelProductionReview('review-1', command({ reason: 'Superseded' }))

  assert.deepEqual(requests.map(request => [request.method, request.url]), [
    ['post', '/api/v1/production/documents/document-1/annotations'],
    ['get', '/api/v1/production/documents/document-1/annotations'],
    ['put', '/api/v1/production/annotations/annotation-1/status'],
    ['post', '/api/v1/production/reviews/review-1/steps/step-1/decision'],
    ['post', '/api/v1/production/reviews/review-1/reject'],
    ['post', '/api/v1/production/reviews/review-1/cancel'],
  ])
  assert.deepEqual(requests[1].params, {
    version_id: 'version-1', status: 'open', annotation_type: 'comment', severity: 'info', page: 2, page_size: 25,
  })
  assert.equal(requests[0].headers['Idempotency-Key'], 'review-command')
  assert.deepEqual(bodyOf(requests[2]), { status: 'resolved' })
  assert.equal(requests[3].headers['Idempotency-Key'], 'review-command')
  assert.deepEqual(bodyOf(requests[3]), { decision: 'approved', comment: 'Ready to publish' })
  assert.deepEqual(bodyOf(requests[4]), { reason: 'Policy mismatch' })
})

test('production commands preserve If-Match, DELETE config, and an empty retry body', async () => {
  requests.length = 0
  const command = <T>(payload: T) => createProductionCommand(payload, () => 'stable-command')

  await production.appendProductionVersion('document-1', 'version-1', command({
    source_set_id: 'source-set-1', blocks: [{ block_type: 'paragraph' }],
  }))
  await production.removeProductionProjectRole('project-1', 'user-1', 'author', command(undefined))
  await production.retryProductionReleaseTarget('target-1', command(undefined))

  assert.equal(requests[0].headers['If-Match'], 'version-1')
  assert.equal(requests[0].headers['Idempotency-Key'], 'stable-command')
  assert.equal(requests[1].method, 'delete')
  assert.equal(requests[1].headers['Idempotency-Key'], 'stable-command')
  assert.equal(requests[2].data, undefined)
  assert.equal(requests[2].headers['Idempotency-Key'], 'stable-command')
})

test('production document detail clients use the authorized detail routes', async () => {
  requests.length = 0

  await production.getProductionDocument('document-1')
  await production.getProductionDocumentVersion('document-1', 'version-1')

  assert.deepEqual(requests.map(request => [request.method, request.url]), [
    ['get', '/api/v1/production/documents/document-1'],
    ['get', '/api/v1/production/documents/document-1/versions/version-1'],
  ])
})
