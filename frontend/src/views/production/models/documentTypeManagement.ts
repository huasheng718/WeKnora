import type {
  CreateProductionDocumentTypeInput,
  DeriveProductionDocumentTypeInput,
  ProductionDocumentType,
  ProductionDocumentTypeOrigin,
  ProductionDocumentTypeStatus,
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

export type ParsedDerivedDocumentTypeDraft =
  | { ok: true; payload: DeriveProductionDocumentTypeInput }
  | { ok: false; reason: 'required' | 'invalid_json'; field: DocumentTypeDraftField }

export interface DocumentTypeConfigurationSummary {
  sections: string[]
  sourceKinds: string[]
  minimumEvidence: number | null
  requiresEvidenceSection: boolean
  reviewSteps: string[]
  publicationTarget: string
  requiresApprovedReview: boolean
}

export interface DocumentTypeControlVisibility {
  inspect: true
  create: boolean
  activate: boolean
  derive: boolean
}

export interface ProductionRequestFailure {
  status: number | null
  message: string
}

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

export function canDeriveProductionDocumentType(
  role: TenantRole | '',
  status: ProductionDocumentTypeStatus,
): boolean {
  return canManageProductionDocumentTypes(role) && (status === 'active' || status === 'retired')
}

export function documentTypeControlVisibility(
  role: TenantRole | '',
  status?: ProductionDocumentTypeStatus,
): DocumentTypeControlVisibility {
  const canManage = canManageProductionDocumentTypes(role)
  return {
    inspect: true,
    create: canManage,
    activate: canManage && status === 'draft',
    derive: canManage && (status === 'active' || status === 'retired'),
  }
}

export function productionRequestFailure(cause: unknown, fallback: string): ProductionRequestFailure {
  const failure = record(cause)
  const response = record(failure?.response)
  const responseData = record(response?.data)
  const nestedError = record(failure?.error)
  const responseError = record(responseData?.error)
  const status = numberValue(failure?.status) ?? numberValue(response?.status)
  const message = firstMessage(
    cause instanceof Error ? cause.message : undefined,
    failure?.message,
    typeof failure?.error === 'string' ? failure.error : undefined,
    nestedError?.message,
    typeof responseData?.error === 'string' ? responseData.error : undefined,
    responseError?.message,
    responseData?.message,
  )
  return { status: status ?? null, message: message ?? fallback }
}

export function reconcileDocumentTypeDerivationBase(
  base: ProductionDocumentType,
  items: ProductionDocumentType[],
): ProductionDocumentType | null {
  const refreshed = items.find(item => item.id === base.id)
  return refreshed && (refreshed.status === 'active' || refreshed.status === 'retired') ? refreshed : null
}

export class DocumentTypeDerivationLifecycle<TCommand> {
  private signature = ''
  private command: TCommand | null = null

  get currentCommand(): TCommand | null {
    return this.command
  }

  prepare(baseID: string, payload: unknown, createCommand: () => TCommand): TCommand {
    const signature = JSON.stringify({ baseID, payload })
    if (this.command && this.signature === signature) return this.command
    this.signature = signature
    this.command = createCommand()
    return this.command
  }

  reset(): void {
    this.signature = ''
    this.command = null
  }

  async fail<TForm>(
    cause: unknown,
    fallback: string,
    form: TForm,
    base: ProductionDocumentType,
    refresh: () => Promise<ProductionDocumentType[] | null>,
  ) {
    const failure = productionRequestFailure(cause, fallback)
    if (failure.status !== 409) {
      return {
        ...failure,
        form,
        preserveInput: true as const,
        command: this.command,
        refreshed: false,
        base,
      }
    }

    const items = await refresh()
    return {
      ...failure,
      form,
      preserveInput: true as const,
      command: this.command,
      refreshed: items !== null,
      base: items === null ? base : reconcileDocumentTypeDerivationBase(base, items),
    }
  }

  async succeed(refresh: () => Promise<ProductionDocumentType[] | null>) {
    this.reset()
    const items = await refresh()
    return { refreshed: items !== null, items }
  }
}

export function documentTypeOriginBadge(origin: ProductionDocumentTypeOrigin): {
  theme: 'primary' | 'default'
  textKey: 'production.documentTypes.origin.builtin' | 'production.documentTypes.origin.custom'
} {
  return origin === 'builtin'
    ? { theme: 'primary', textKey: 'production.documentTypes.origin.builtin' }
    : { theme: 'default', textKey: 'production.documentTypes.origin.custom' }
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

  const parsed = parseDocumentTypeJSONFields(form)
  if (!parsed.ok) return parsed

  return {
    ok: true,
    payload: {
      code,
      name,
      description: form.description.trim(),
      schema_version: schemaVersion,
      block_schema: parsed.values.block_schema,
      source_requirements: parsed.values.source_requirements,
      skill_bindings: parsed.values.skill_bindings,
      workflow_plan: parsed.values.workflow_plan,
      quality_rules: parsed.values.quality_rules,
      review_policy: parsed.values.review_policy,
      publication_policy: parsed.values.publication_policy,
    },
  }
}

export function parseDerivedDocumentTypeDraft(form: DocumentTypeDraftForm): ParsedDerivedDocumentTypeDraft {
  const name = form.name.trim()
  if (!name) return { ok: false, reason: 'required', field: 'name' }

  const parsed = parseDocumentTypeJSONFields(form)
  if (!parsed.ok) return parsed

  return {
    ok: true,
    payload: {
      name,
      description: form.description.trim(),
      block_schema: parsed.values.block_schema,
      source_requirements: parsed.values.source_requirements,
      skill_bindings: parsed.values.skill_bindings,
      workflow_plan: parsed.values.workflow_plan,
      quality_rules: parsed.values.quality_rules,
      review_policy: parsed.values.review_policy,
      publication_policy: parsed.values.publication_policy,
    },
  }
}

export function prefillDerivedDocumentTypeForm(item: ProductionDocumentType): DocumentTypeDraftForm {
  return {
    code: item.code,
    name: item.name,
    description: item.description,
    schemaVersion: item.schema_version,
    blockSchema: formatDocumentTypeJSON(item.block_schema),
    sourceRequirements: formatDocumentTypeJSON(item.source_requirements),
    skillBindings: formatDocumentTypeJSON(item.skill_bindings),
    workflowPlan: formatDocumentTypeJSON(item.workflow_plan),
    qualityRules: formatDocumentTypeJSON(item.quality_rules),
    reviewPolicy: formatDocumentTypeJSON(item.review_policy),
    publicationPolicy: formatDocumentTypeJSON(item.publication_policy),
  }
}

export function formatDocumentTypeJSON(value: ProductionJSON): string {
  return JSON.stringify(value, null, 2)
}

export function documentTypeConfigurationSummary(item: ProductionDocumentType): DocumentTypeConfigurationSummary {
  const blockSchema = jsonObject(item.block_schema)
  const sources = jsonObject(item.source_requirements)
  const review = jsonObject(item.review_policy)
  const publication = jsonObject(item.publication_policy)
  return {
    sections: stringList(blockSchema.required_sections),
    sourceKinds: stringList(sources.allowed_source_kinds),
    minimumEvidence: typeof sources.min_accepted_evidence === 'number' ? sources.min_accepted_evidence : null,
    requiresEvidenceSection: sources.require_evidence_section === true,
    reviewSteps: stringList(review.steps),
    publicationTarget: typeof publication.target_type === 'string' ? publication.target_type : '',
    requiresApprovedReview: publication.require_approved_review === true,
  }
}

function parseDocumentTypeJSONFields(form: DocumentTypeDraftForm):
  | { ok: true; values: Record<(typeof JSON_FIELDS)[number][1], ProductionJSON> }
  | { ok: false; reason: 'invalid_json'; field: DocumentTypeDraftField } {
  const values = {} as Record<(typeof JSON_FIELDS)[number][1], ProductionJSON>
  for (const [formField, apiField] of JSON_FIELDS) {
    try {
      values[apiField] = JSON.parse(form[formField].trim() || '{}') as ProductionJSON
    } catch {
      return { ok: false, reason: 'invalid_json', field: formField }
    }
  }
  return { ok: true, values }
}

function jsonObject(value: ProductionJSON): Record<string, ProductionJSON> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value : {}
}

function stringList(value: ProductionJSON | undefined): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' ? value as Record<string, unknown> : null
}

function numberValue(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function firstMessage(...values: unknown[]): string | null {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return null
}

export function sortDocumentTypeVersions(items: ProductionDocumentType[]): ProductionDocumentType[] {
  return [...items].sort((left, right) => {
    const codeOrder = left.code.localeCompare(right.code)
    return codeOrder === 0 ? right.schema_version - left.schema_version : codeOrder
  })
}
