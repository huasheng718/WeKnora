import type {
  CreateProductionDocumentTypeInput,
  ProductionDocumentType,
  ProductionJSON,
} from '@/api/production'
import type { TenantRole } from '@/api/tenant/members'

export type DocumentTypeListViewState = 'loading' | 'error' | 'empty' | 'ready'

export interface DocumentTypeListStateInput {
  loading: boolean
  loaded: boolean
  error: string
  itemCount: number
}

export interface DocumentTypeDraftForm {
  code: string
  name: string
  description: string
  schemaVersion: number | string
  blockSchema: string
  sourceRequirements: string
  skillBindings: string
  workflowPlan: string
  qualityRules: string
  reviewPolicy: string
  publicationPolicy: string
}

type DocumentTypeDraftField = keyof DocumentTypeDraftForm

export type ParsedDocumentTypeDraft =
  | { ok: true; payload: CreateProductionDocumentTypeInput }
  | { ok: false; reason: 'required' | 'schema_version' | 'invalid_json'; field: DocumentTypeDraftField }

const JSON_FIELDS = [
  ['blockSchema', 'block_schema'],
  ['sourceRequirements', 'source_requirements'],
  ['skillBindings', 'skill_bindings'],
  ['workflowPlan', 'workflow_plan'],
  ['qualityRules', 'quality_rules'],
  ['reviewPolicy', 'review_policy'],
  ['publicationPolicy', 'publication_policy'],
] as const

export function canManageProductionDocumentTypes(role: TenantRole | ''): boolean {
  return role === 'admin' || role === 'owner'
}

export function documentTypeListViewState(input: DocumentTypeListStateInput): DocumentTypeListViewState {
  if (input.loading && !input.loaded) return 'loading'
  if (input.error) return 'error'
  return input.itemCount === 0 ? 'empty' : 'ready'
}

export function parseDocumentTypeDraft(form: DocumentTypeDraftForm): ParsedDocumentTypeDraft {
  const code = form.code.trim()
  const name = form.name.trim()
  if (!code) return { ok: false, reason: 'required', field: 'code' }
  if (!name) return { ok: false, reason: 'required', field: 'name' }

  const schemaVersion = Number(form.schemaVersion)
  if (!Number.isInteger(schemaVersion) || schemaVersion < 1) {
    return { ok: false, reason: 'schema_version', field: 'schemaVersion' }
  }

  const parsed = {} as Record<(typeof JSON_FIELDS)[number][1], ProductionJSON>
  for (const [formField, apiField] of JSON_FIELDS) {
    try {
      parsed[apiField] = JSON.parse(form[formField].trim() || '{}') as ProductionJSON
    } catch {
      return { ok: false, reason: 'invalid_json', field: formField }
    }
  }

  return {
    ok: true,
    payload: {
      code,
      name,
      description: form.description.trim(),
      schema_version: schemaVersion,
      block_schema: parsed.block_schema,
      source_requirements: parsed.source_requirements,
      skill_bindings: parsed.skill_bindings,
      workflow_plan: parsed.workflow_plan,
      quality_rules: parsed.quality_rules,
      review_policy: parsed.review_policy,
      publication_policy: parsed.publication_policy,
    },
  }
}

export function sortDocumentTypeVersions(items: ProductionDocumentType[]): ProductionDocumentType[] {
  return [...items].sort((left, right) => {
    const codeOrder = left.code.localeCompare(right.code)
    return codeOrder === 0 ? right.schema_version - left.schema_version : codeOrder
  })
}
