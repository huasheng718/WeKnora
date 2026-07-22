import type { ProductionJSON } from '@/api/production'

export function productionReleaseConfigRows(value: ProductionJSON): [string, string][] {
  if (!value || Array.isArray(value) || typeof value !== 'object') return []

  const object = value as Record<string, ProductionJSON>
  const chunking = object.chunking as Record<string, ProductionJSON> | undefined
  const graph = object.graph as Record<string, ProductionJSON> | undefined
  const extraction = graph?.extract_config as Record<string, ProductionJSON> | undefined

  return [
    ['chunking', String(chunking?.strategy ?? '-')],
    ['size', String(chunking?.chunk_size ?? '-')],
    ['overlap', String(chunking?.chunk_overlap ?? '-')],
    ['separators', JSON.stringify(chunking?.separators ?? [])],
    ['embedding', String(object.embedding_model_id ?? '-')],
    ['graph', String(graph?.enabled ?? false)],
    ['graph model', String(graph?.model_id ?? '-')],
    ['extraction', JSON.stringify(extraction ?? {})],
  ]
}
