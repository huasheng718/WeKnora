import assert from 'node:assert/strict'
import test from 'node:test'
import { productionReleaseConfigRows } from './releaseConfigRows'

test('release preview maps the exact ready snapshot backend contract without defaults', () => {
  assert.deepEqual(productionReleaseConfigRows({
    chunking: {
      strategy: 'recursive',
      chunk_size: 768,
      chunk_overlap: 96,
      separators: ['\n\n', '\n', '。'],
    },
    embedding_model_id: 'embedding-real-1',
    graph: {
      enabled: true,
      model_id: 'graph-real-1',
      extract_config: {
        entity_types: ['person', 'project'],
        relation_types: ['owns'],
        max_depth: 3,
      },
    },
  }), [
    ['chunking', 'recursive'],
    ['size', '768'],
    ['overlap', '96'],
    ['separators', '["\\n\\n","\\n","。"]'],
    ['embedding', 'embedding-real-1'],
    ['graph', 'true'],
    ['graph model', 'graph-real-1'],
    ['extraction', '{"entity_types":["person","project"],"relation_types":["owns"],"max_depth":3}'],
  ])
})

test('release preview preserves explicit false and zero values', () => {
  const rows = productionReleaseConfigRows({
    chunking: { strategy: 'fixed', chunk_size: 0, chunk_overlap: 0, separators: [] },
    embedding_model_id: 'embedding-zero',
    graph: { enabled: false, model_id: '', extract_config: {} },
  })

  assert.deepEqual(Object.fromEntries(rows), {
    chunking: 'fixed',
    size: '0',
    overlap: '0',
    separators: '[]',
    embedding: 'embedding-zero',
    graph: 'false',
    'graph model': '',
    extraction: '{}',
  })
})
