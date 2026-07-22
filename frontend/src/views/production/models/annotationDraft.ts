import type {
  CreateProductionAnnotationInput,
  ProductionAnnotation,
} from '@/api/production'
import {
  createProductionCommand,
  type ProductionCommand,
} from '@/api/production/idempotency'

export interface ProductionAnnotationDraft {
  body: string
  severity: ProductionAnnotation['severity']
}

export interface ProductionAnnotationAnchor {
  versionId: string
  blockId: string
  logicalBlockId: string
}

export interface PendingAnnotationSubmission {
  signature: string
  command: ProductionCommand<CreateProductionAnnotationInput>
}

export function buildAnnotationSubmission(
  draft: ProductionAnnotationDraft,
  anchor: ProductionAnnotationAnchor,
  current: PendingAnnotationSubmission | null,
  createKey?: () => string,
): PendingAnnotationSubmission {
  const payload: CreateProductionAnnotationInput = {
    version_id: anchor.versionId,
    block_id: anchor.blockId,
    annotation_type: 'comment',
    severity: draft.severity,
    anchor: { logical_block_id: anchor.logicalBlockId },
    body: draft.body.trim(),
  }
  const signature = JSON.stringify(payload)
  if (current?.signature === signature) return current
  return { signature, command: createProductionCommand(payload, createKey) }
}

export function completeAnnotationDraft(draft: ProductionAnnotationDraft): ProductionAnnotationDraft {
  return { ...draft, body: '' }
}
