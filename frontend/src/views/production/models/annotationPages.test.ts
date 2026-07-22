import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionAnnotation, ProductionAnnotationListResponse } from '@/api/production'
import { loadProductionAnnotationPages } from './annotationPages'

function annotation(id: string, severity: ProductionAnnotation['severity'] = 'info'): ProductionAnnotation {
  return {
    id,
    tenant_id: 1,
    project_id: 'project-1',
    document_id: 'document-1',
    version_id: 'version-1',
    block_id: 'block-1',
    annotation_type: 'comment',
    severity,
    anchor: {},
    status: 'open',
    body: id,
    created_by: 'reviewer-1',
    created_at: '2026-07-22T00:00:00Z',
    updated_at: '2026-07-22T00:00:00Z',
  }
}

function page(data: ProductionAnnotation[], currentPage: number, hasMore: boolean): ProductionAnnotationListResponse {
  return { success: true, data, page: currentPage, page_size: 100, total: 101, has_more: hasMore }
}

test('loads the page-two blocking annotation that otherwise cannot be resolved from the workbench', async () => {
  const firstPage = Array.from({ length: 100 }, (_, index) => annotation(`annotation-${index + 1}`))
  const blocking = annotation('annotation-101', 'blocking')
  const requestedPages: number[] = []

  const annotations = await loadProductionAnnotationPages(async currentPage => {
    requestedPages.push(currentPage)
    return currentPage === 1 ? page(firstPage, 1, true) : page([blocking], 2, false)
  })

  assert.deepEqual(requestedPages, [1, 2])
  assert.deepEqual(annotations.map(row => row.id), [...firstPage, blocking].map(row => row.id))
  assert.equal(annotations.filter(row => row.status === 'open' && row.severity === 'blocking').length, 1)
})

test('deduplicates annotation overlap between pages without changing first-seen order', async () => {
  const first = annotation('annotation-1')
  const shifted = annotation('annotation-2', 'blocking')
  const last = annotation('annotation-3')

  const annotations = await loadProductionAnnotationPages(async currentPage => currentPage === 1
    ? page([first, shifted], 1, true)
    : page([shifted, last], 2, false))

  assert.deepEqual(annotations.map(row => row.id), [first.id, shifted.id, last.id])
  assert.equal(annotations.filter(row => row.status === 'open' && row.severity === 'blocking').length, 1)
})

test('stops at the server offset boundary before requesting an invalid page', async () => {
  const requestedPages: number[] = []

  await assert.rejects(
    loadProductionAnnotationPages(async currentPage => {
      requestedPages.push(currentPage)
      return page([annotation(`annotation-${currentPage}`)], currentPage, true)
    }),
    /server offset limit/,
  )

  assert.deepEqual(requestedPages, Array.from({ length: 101 }, (_, index) => index + 1))
})
