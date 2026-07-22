import assert from 'node:assert/strict'
import test from 'node:test'

import type { ProductionDocumentVersionDetail } from '@/api/production'
import {
  applyDocumentBlockOperation,
  buildVersionPayload,
  defaultBlockContent,
  summarizeVersionDiff,
  versionDraftBlocks,
} from './documentBlocks'

function versionFixture(): ProductionDocumentVersionDetail {
  return {
    id: '11111111-1111-4111-8111-111111111111',
    document_id: '22222222-2222-4222-8222-222222222222',
    tenant_id: 7,
    project_id: '33333333-3333-4333-8333-333333333333',
    version_number: 3,
    source_set_id: '44444444-4444-4444-8444-444444444444',
    origin: 'human',
    change_summary: 'Current version',
    content_digest: 'digest',
    created_by: '55555555-5555-4555-8555-555555555555',
    created_at: '2026-07-22T10:00:00Z',
    blocks: [
      {
        id: '66666666-6666-4666-8666-666666666666',
        version_id: '11111111-1111-4111-8111-111111111111',
        logical_block_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
        block_type: 'paragraph',
        position: 0,
        content: 'Original paragraph',
        attributes: { tone: 'plain' },
        evidence_refs: ['77777777-7777-4777-8777-777777777777'],
        ai_provenance: {},
        content_digest: 'block-digest-a',
      },
      {
        id: '88888888-8888-4888-8888-888888888888',
        version_id: '11111111-1111-4111-8111-111111111111',
        logical_block_id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
        block_type: 'heading',
        position: 1,
        content: 'Existing heading',
        attributes: { level: 2 },
        evidence_refs: [],
        ai_provenance: { run_id: '99999999-9999-4999-8999-999999999999' },
        content_digest: 'block-digest-b',
      },
    ],
    lineage: [],
  }
}

test('unchanged blocks retain logical ids in the next version payload', () => {
  const source = versionFixture()
  const next = buildVersionPayload(source, [
    { op: 'edit', logicalBlockId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', content: 'Revised paragraph' },
  ])

  assert.equal(next.blocks[0].logical_block_id, 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')
  assert.equal(next.blocks[1].logical_block_id, 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb')
  assert.equal(next.blocks[0].content, 'Revised paragraph')
})

test('new blocks receive ids but never mutate the source version', () => {
  const source = versionFixture()
  const sourceSnapshot = structuredClone(source)
  const next = buildVersionPayload(
    source,
    [{ op: 'insertAfter', after: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', blockType: 'paragraph' }],
    { createId: () => 'cccccccc-cccc-4ccc-8ccc-cccccccccccc' },
  )

  assert.deepEqual(source, sourceSnapshot)
  assert.equal(source.blocks.length, 2)
  assert.equal(next.blocks.length, 3)
  assert.equal(next.blocks[1].logical_block_id, 'cccccccc-cccc-4ccc-8ccc-cccccccccccc')
  assert.equal(next.blocks[1].content, '')
})

test('delete move edit and type changes keep valid block content schemas', () => {
  let blocks = versionDraftBlocks(versionFixture())
  blocks = applyDocumentBlockOperation(blocks, {
    op: 'changeType',
    logicalBlockId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    blockType: 'table',
  })
  blocks = applyDocumentBlockOperation(blocks, {
    op: 'edit',
    logicalBlockId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    content: { headers: ['Item', 'Owner'], rows: [['Draft', 'Lin']] },
  })
  blocks = applyDocumentBlockOperation(blocks, {
    op: 'move',
    logicalBlockId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    toIndex: 1,
  })
  blocks = applyDocumentBlockOperation(blocks, {
    op: 'delete',
    logicalBlockId: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
  })

  assert.equal(blocks.length, 1)
  assert.equal(blocks[0].block_type, 'table')
  assert.deepEqual(blocks[0].content, { headers: ['Item', 'Owner'], rows: [['Draft', 'Lin']] })
  assert.deepEqual(defaultBlockContent('list'), [])
  assert.deepEqual(defaultBlockContent('image'), { alt: '', url: '' })
})

test('evidence links are unique and can be removed without mutating input', () => {
  const source = versionDraftBlocks(versionFixture())
  const linked = applyDocumentBlockOperation(source, {
    op: 'linkEvidence',
    logicalBlockId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    evidenceId: '77777777-7777-4777-8777-777777777777',
  })
  const unlinked = applyDocumentBlockOperation(linked, {
    op: 'unlinkEvidence',
    logicalBlockId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    evidenceId: '77777777-7777-4777-8777-777777777777',
  })

  assert.deepEqual(source[0].evidence_refs, ['77777777-7777-4777-8777-777777777777'])
  assert.deepEqual(linked[0].evidence_refs, ['77777777-7777-4777-8777-777777777777'])
  assert.deepEqual(unlinked[0].evidence_refs, [])
})

test('append payload matches the strict backend contract and omits persisted row fields', () => {
  const next = buildVersionPayload(versionFixture(), [], {
    changeSummary: 'Clarify ownership',
    origin: 'human',
  })

  assert.deepEqual(Object.keys(next).sort(), ['blocks', 'change_summary', 'origin', 'source_set_id'])
  assert.equal(next.source_set_id, '44444444-4444-4444-8444-444444444444')
  assert.equal(next.change_summary, 'Clarify ownership')
  assert.deepEqual(Object.keys(next.blocks[0]).sort(), [
    'ai_provenance',
    'attributes',
    'block_type',
    'content',
    'evidence_refs',
    'logical_block_id',
  ])
  assert.equal('id' in next.blocks[0], false)
  assert.equal('position' in next.blocks[0], false)
  assert.equal('content_digest' in next.blocks[0], false)
})

test('the draft cannot delete its final block or create duplicate logical ids', () => {
  const oneBlock = versionDraftBlocks({ ...versionFixture(), blocks: [versionFixture().blocks[0]] })

  assert.throws(
    () => applyDocumentBlockOperation(oneBlock, {
      op: 'delete',
      logicalBlockId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    }),
    /at least one block/,
  )
  assert.throws(
    () => buildVersionPayload(versionFixture(), [
      { op: 'insertAfter', after: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', blockType: 'paragraph' },
    ], { createId: () => 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' }),
    /logical block ids must be unique/,
  )
})

test('version diff counts stable logical ids as added changed removed or unchanged', () => {
  const base = versionFixture()
  const target = structuredClone(base)
  target.blocks = [
    { ...target.blocks[0], content: 'Changed', content_digest: 'changed-digest' },
    {
      ...target.blocks[1],
      id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
      logical_block_id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
      content_digest: 'new-digest',
    },
  ]

  assert.deepEqual(summarizeVersionDiff(base, target), {
    added: 1,
    changed: 1,
    removed: 1,
    unchanged: 0,
  })
})

test('table and image blocks reject unknown content fields', () => {
  const table = versionDraftBlocks(versionFixture())
  table[0].block_type = 'table'
  table[0].content = { headers: ['A'], rows: [['value']], extra: true }
  assert.throws(() => applyDocumentBlockOperation(table, {
    op: 'edit', logicalBlockId: table[0].logical_block_id, content: table[0].content,
  }), /invalid table/)

  const image = versionDraftBlocks(versionFixture())
  image[0].block_type = 'image'
  assert.throws(() => applyDocumentBlockOperation(image, {
    op: 'edit', logicalBlockId: image[0].logical_block_id,
    content: { alt: 'Diagram', url: 'https://example.com/diagram.png', caption: 'extra' },
  }), /invalid image/)
})
