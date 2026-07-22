import type {
  AppendProductionVersionInput,
  ProductionDocumentVersionDetail,
  ProductionJSON,
} from '@/api/production'

export type ProductionEditorBlockType = 'heading' | 'paragraph' | 'code' | 'callout' | 'list' | 'table' | 'image'

export interface ProductionDraftBlock {
  logical_block_id: string
  block_type: ProductionEditorBlockType
  content: ProductionJSON
  attributes: ProductionJSON
  evidence_refs: string[]
  ai_provenance: ProductionJSON
}

export interface ProductionVersionDiff {
  added: number
  changed: number
  removed: number
  unchanged: number
}

export type DocumentBlockOperation =
  | { op: 'edit'; logicalBlockId: string; content: ProductionJSON }
  | { op: 'insertAfter'; after?: string | null; blockType: ProductionEditorBlockType; content?: ProductionJSON }
  | { op: 'delete'; logicalBlockId: string }
  | { op: 'move'; logicalBlockId: string; toIndex: number }
  | { op: 'changeType'; logicalBlockId: string; blockType: ProductionEditorBlockType }
  | { op: 'linkEvidence'; logicalBlockId: string; evidenceId: string }
  | { op: 'unlinkEvidence'; logicalBlockId: string; evidenceId: string }

interface BuildVersionPayloadOptions {
  createId?: () => string
  changeSummary?: string
  origin?: NonNullable<AppendProductionVersionInput['origin']>
}

function cloneJSON<T extends ProductionJSON>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function objectJSON(value: ProductionJSON): ProductionJSON {
  return value && typeof value === 'object' && !Array.isArray(value) ? cloneJSON(value) : {}
}

function evidenceIDs(value: ProductionJSON): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((row): row is string => typeof row === 'string')
}

function isEditorBlockType(value: string): value is ProductionEditorBlockType {
  return ['heading', 'paragraph', 'code', 'callout', 'list', 'table', 'image'].includes(value)
}

function hasExactKeys(value: object, expected: readonly string[]): boolean {
  const keys = Object.keys(value).sort()
  return keys.length === expected.length && keys.every((key, index) => key === expected[index])
}

export function defaultBlockContent(blockType: ProductionEditorBlockType): ProductionJSON {
  if (blockType === 'list') return []
  if (blockType === 'table') return { headers: ['Column 1'], rows: [] }
  if (blockType === 'image') return { alt: '', url: '' }
  return ''
}

export function isValidBlockContent(blockType: ProductionEditorBlockType, content: ProductionJSON): boolean {
  if (blockType === 'heading' || blockType === 'paragraph' || blockType === 'code' || blockType === 'callout') {
    return typeof content === 'string'
  }
  if (blockType === 'list') {
    return Array.isArray(content) && content.every(item => typeof item === 'string')
  }
  if (blockType === 'table') {
    if (!content || typeof content !== 'object' || Array.isArray(content)) return false
    if (!hasExactKeys(content, ['headers', 'rows'])) return false
	const table = content as { headers?: unknown; rows?: unknown }
	if (!Array.isArray(table.headers) || table.headers.length === 0 || !table.headers.every(item => typeof item === 'string')) return false
	const headers = table.headers
	return Array.isArray(table.rows) && table.rows.every(row =>
		Array.isArray(row) && row.length === headers.length && row.every(cell => typeof cell === 'string'),
	)
  }
  if (!content || typeof content !== 'object' || Array.isArray(content)) return false
  if (!hasExactKeys(content, ['alt', 'url'])) return false
  const image = content as { alt?: unknown; url?: unknown }
  if (typeof image.alt !== 'string' || typeof image.url !== 'string') return false
  try {
    const parsed = new URL(image.url)
    return (parsed.protocol === 'https:' || parsed.protocol === 'http:')
      && !!parsed.host
      && !parsed.username
      && !parsed.password
      && !parsed.search
      && !parsed.hash
  } catch {
    return false
  }
}

export function versionDraftBlocks(version: ProductionDocumentVersionDetail): ProductionDraftBlock[] {
  return [...version.blocks]
    .sort((left, right) => left.position - right.position)
    .map(block => {
      if (!isEditorBlockType(block.block_type)) {
        throw new Error(`unsupported block type: ${block.block_type}`)
      }
      return {
        logical_block_id: block.logical_block_id,
        block_type: block.block_type,
        content: cloneJSON(block.content),
        attributes: objectJSON(block.attributes),
        evidence_refs: evidenceIDs(block.evidence_refs),
        ai_provenance: objectJSON(block.ai_provenance),
      }
    })
}

export function summarizeVersionDiff(
  base: Pick<ProductionDocumentVersionDetail, 'blocks'>,
  target: Pick<ProductionDocumentVersionDetail, 'blocks'>,
): ProductionVersionDiff {
  const baseByLogicalID = new Map(base.blocks.map(block => [block.logical_block_id, block]))
  const targetByLogicalID = new Map(target.blocks.map(block => [block.logical_block_id, block]))
  const summary: ProductionVersionDiff = { added: 0, changed: 0, removed: 0, unchanged: 0 }

  for (const [logicalID, block] of targetByLogicalID) {
    const previous = baseByLogicalID.get(logicalID)
    if (!previous) summary.added++
    else if (previous.content_digest !== block.content_digest) summary.changed++
    else summary.unchanged++
  }
  for (const logicalID of baseByLogicalID.keys()) {
    if (!targetByLogicalID.has(logicalID)) summary.removed++
  }
  return summary
}

function cloneDraftBlocks(blocks: readonly ProductionDraftBlock[]): ProductionDraftBlock[] {
  return blocks.map(block => ({
    ...block,
    content: cloneJSON(block.content),
    attributes: cloneJSON(block.attributes),
    evidence_refs: [...block.evidence_refs],
    ai_provenance: cloneJSON(block.ai_provenance),
  }))
}

function requireBlockIndex(blocks: readonly ProductionDraftBlock[], logicalBlockId: string): number {
  const index = blocks.findIndex(block => block.logical_block_id === logicalBlockId)
  if (index < 0) throw new Error(`logical block not found: ${logicalBlockId}`)
  return index
}

function assertUniqueLogicalIDs(blocks: readonly ProductionDraftBlock[]): void {
  const ids = new Set<string>()
  for (const block of blocks) {
    if (!block.logical_block_id || block.logical_block_id.length > 36 || ids.has(block.logical_block_id)) {
      throw new Error('logical block ids must be unique and contain at most 36 characters')
    }
    ids.add(block.logical_block_id)
  }
}

export function applyDocumentBlockOperation(
  input: readonly ProductionDraftBlock[],
  operation: DocumentBlockOperation,
  createId: () => string = () => crypto.randomUUID(),
): ProductionDraftBlock[] {
  const blocks = cloneDraftBlocks(input)

  if (operation.op === 'insertAfter') {
    const logicalBlockId = createId()
    const insertIndex = operation.after == null
      ? 0
      : requireBlockIndex(blocks, operation.after) + 1
    blocks.splice(insertIndex, 0, {
      logical_block_id: logicalBlockId,
      block_type: operation.blockType,
      content: cloneJSON(operation.content ?? defaultBlockContent(operation.blockType)),
      attributes: {},
      evidence_refs: [],
      ai_provenance: {},
    })
    assertUniqueLogicalIDs(blocks)
    return blocks
  }

  const index = requireBlockIndex(blocks, operation.logicalBlockId)
  if (operation.op === 'edit') {
    if (!isValidBlockContent(blocks[index].block_type, operation.content)) {
      throw new Error(`invalid ${blocks[index].block_type} block content`)
    }
    blocks[index].content = cloneJSON(operation.content)
  } else if (operation.op === 'delete') {
    if (blocks.length === 1) throw new Error('a document version requires at least one block')
    blocks.splice(index, 1)
  } else if (operation.op === 'move') {
    const [block] = blocks.splice(index, 1)
    const target = Math.max(0, Math.min(operation.toIndex, blocks.length))
    blocks.splice(target, 0, block)
  } else if (operation.op === 'changeType') {
    blocks[index].block_type = operation.blockType
    blocks[index].content = defaultBlockContent(operation.blockType)
  } else if (operation.op === 'linkEvidence') {
    if (!blocks[index].evidence_refs.includes(operation.evidenceId)) {
      blocks[index].evidence_refs.push(operation.evidenceId)
    }
  } else {
    blocks[index].evidence_refs = blocks[index].evidence_refs.filter(id => id !== operation.evidenceId)
  }

  assertUniqueLogicalIDs(blocks)
  return blocks
}

export function draftBlocksPayload(
  blocks: readonly ProductionDraftBlock[],
  sourceSetId: string,
  options: Omit<BuildVersionPayloadOptions, 'createId'> = {},
): AppendProductionVersionInput {
  if (blocks.length === 0) throw new Error('a document version requires at least one block')
  assertUniqueLogicalIDs(blocks)
  for (const block of blocks) {
    if (!isValidBlockContent(block.block_type, block.content)) {
      throw new Error(`invalid ${block.block_type} block content`)
    }
  }
  return {
    source_set_id: sourceSetId,
    origin: options.origin ?? 'human',
    change_summary: options.changeSummary?.trim() ?? '',
    blocks: blocks.map(block => ({
      logical_block_id: block.logical_block_id,
      block_type: block.block_type,
      content: cloneJSON(block.content),
      attributes: cloneJSON(block.attributes),
      evidence_refs: [...block.evidence_refs],
      ai_provenance: cloneJSON(block.ai_provenance),
    })),
  }
}

export function buildVersionPayload(
  source: ProductionDocumentVersionDetail,
  operations: readonly DocumentBlockOperation[],
  options: BuildVersionPayloadOptions = {},
): AppendProductionVersionInput {
  const createId = options.createId ?? (() => crypto.randomUUID())
  const blocks = operations.reduce(
    (current, operation) => applyDocumentBlockOperation(current, operation, createId),
    versionDraftBlocks(source),
  )
  return draftBlocksPayload(blocks, source.source_set_id, options)
}
