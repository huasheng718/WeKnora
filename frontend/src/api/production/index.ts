import { del, get, post, postEmpty, put } from '@/utils/request'
import {
  type ProductionCommand,
  productionCommandConfig,
} from './idempotency'

export type ProductionJSON = Record<string, unknown>
export type ProductionTimestamp = string

export interface ProductionResponse<T> {
  success: boolean
  data?: T
  message?: string
}

export type ProductionProjectStatus = 'active' | 'archived'
export type ProductionProjectRole =
  | 'project_owner'
  | 'author'
  | 'business_reviewer'
  | 'engineering_reviewer'
  | 'knowledge_admin'
  | 'compliance_reviewer'
  | 'publisher'
  | 'observer'

export interface ProductionProject {
  id: string
  tenant_id: number
  name: string
  description: string
  owner_user_id: string
  status: ProductionProjectStatus
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface ProductionProjectMember {
  project_id: string
  user_id: string
  role: ProductionProjectRole
  assigned_by: string
  created_at: ProductionTimestamp
}

export interface CreateProductionProjectInput {
  name: string
  description?: string
}

export interface AssignProductionProjectRoleInput {
  user_id: string
  role: ProductionProjectRole
}

export type ProductionDocumentTypeStatus = 'draft' | 'active' | 'retired'

export interface ProductionDocumentType {
  id: string
  tenant_id: number
  code: string
  name: string
  description: string
  schema_version: number
  block_schema: ProductionJSON
  source_requirements: ProductionJSON
  skill_bindings: ProductionJSON
  workflow_plan: ProductionJSON
  quality_rules: ProductionJSON
  review_policy: ProductionJSON
  publication_policy: ProductionJSON
  status: ProductionDocumentTypeStatus
  created_by: string
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface CreateProductionDocumentTypeInput {
  code: string
  name: string
  description?: string
  schema_version: number
  block_schema: ProductionJSON
  source_requirements: ProductionJSON
  skill_bindings: ProductionJSON
  workflow_plan?: ProductionJSON
  quality_rules: ProductionJSON
  review_policy: ProductionJSON
  publication_policy: ProductionJSON
}

export type ProductionSourceSetStatus = 'collecting' | 'ready' | 'failed' | 'frozen'
export type ProductionSourceItemStatus = 'candidate' | 'accepted' | 'rejected' | 'unavailable'

export interface ProductionSourceSet {
  id: string
  tenant_id: number
  project_id: string
  document_type_id: string
  time_range_start?: ProductionTimestamp
  time_range_end?: ProductionTimestamp
  status: ProductionSourceSetStatus
  created_by: string
  created_at: ProductionTimestamp
  frozen_at?: ProductionTimestamp
}

export interface ProductionSourceItem {
  id: string
  source_set_id: string
  source_kind: 'upload' | 'datasource' | 'mcp' | 'skill' | 'manual'
  source_system: string
  external_id: string
  source_uri?: string
  title: string
  mime_type: string
  content_digest: string
  captured_at: ProductionTimestamp
  metadata: ProductionJSON
  status: ProductionSourceItemStatus
  created_at: ProductionTimestamp
}

export interface CreateProductionSourceSetInput {
  document_type_id: string
  time_range_start?: ProductionTimestamp
  time_range_end?: ProductionTimestamp
}

export interface DecideProductionSourceItemInput {
  decision: Extract<ProductionSourceItemStatus, 'accepted' | 'rejected' | 'unavailable'>
}

export type ProductionDocumentStatus =
  | 'draft'
  | 'annotating'
  | 'in_review'
  | 'approved'
  | 'publishing'
  | 'published'
  | 'archived'

export interface ProductionDocument {
  id: string
  tenant_id: number
  project_id: string
  document_type_id: string
  document_type_schema_version: number
  title: string
  current_version_id?: string
  latest_approved_version_id?: string
  status: ProductionDocumentStatus
  created_by: string
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface ProductionDocumentBlock {
  id: string
  version_id: string
  logical_block_id: string
  block_type: string
  position: number
  content: unknown
  attributes: unknown
  evidence_refs: unknown
  ai_provenance: unknown
  content_digest: string
}

export interface ProductionDocumentVersion {
  id: string
  document_id: string
  tenant_id: number
  project_id: string
  version_number: number
  parent_version_id?: string
  source_set_id: string
  origin: 'ai' | 'human' | 'mixed' | 'rollback'
  change_summary: string
  content_digest: string
  created_by: string
  created_at: ProductionTimestamp
  frozen_at?: ProductionTimestamp
  blocks?: ProductionDocumentBlock[]
}

export interface CreateProductionDocumentInput {
  document_type_id: string
  source_set_id: string
  title: string
}

export interface AppendProductionVersionInput {
  source_set_id: string
  origin?: ProductionDocumentVersion['origin']
  change_summary?: string
  blocks: Array<{
    logical_block_id?: string
    block_type: string
    content?: unknown
    attributes?: unknown
    evidence_refs?: unknown
    ai_provenance?: unknown
  }>
  lineage?: Array<{
    from_logical_block_id: string
    to_logical_block_id: string
    relation: 'same' | 'split' | 'merged'
  }>
}

export type ProductionRunType = 'collect' | 'write' | 'rewrite' | 'validate'
export type ProductionRunStatus = 'queued' | 'running' | 'waiting_approval' | 'completed' | 'failed' | 'cancelled'

export interface ProductionRun {
  id: string
  tenant_id: number
  project_id: string
  document_id?: string
  source_set_id: string
  run_type: ProductionRunType
  status: ProductionRunStatus
  attempt: number
  current_step: number
  model_id: string
  output_version_id?: string
  error_code?: string
  error_message?: string
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface ProductionToolCall {
  id: string
  run_id: string
  status: 'planned' | 'pending_approval' | 'approved' | 'rejected' | 'executing' | 'completed' | 'failed'
  approval_status: 'not_required' | 'pending' | 'approved' | 'rejected'
  provider_type: 'skill' | 'mcp' | 'datasource'
  provider_id: string
  tool_name: string
}

export interface StartProductionDocumentRunInput {
  run_type: Exclude<ProductionRunType, 'collect'>
  model_id: string
}

export interface StartProductionCollectionInput {
  model_id: string
}

export interface DecideProductionToolCallInput {
  decision: 'approve' | 'reject'
}

export interface ProductionAnnotation {
  id: string
  document_id: string
  version_id: string
  block_id: string
  annotation_type: 'comment' | 'suggestion' | 'quality_tag'
  severity: 'info' | 'warning' | 'blocking'
  status: 'open' | 'resolved' | 'dismissed'
  body: string
}

export interface ProductionReview {
  id: string
  project_id: string
  document_id: string
  version_id: string
  status: 'pending' | 'approved' | 'rejected' | 'obsolete' | 'cancelled' | 'changes_requested'
  steps?: ProductionReviewStep[]
}

export interface ProductionReviewStep {
  id: string
  review_request_id: string
  required_role: ProductionProjectRole
  decision: 'pending' | 'approved' | 'rejected' | 'cancelled' | 'changes_requested'
  comment: string
}

export type ProductionReleaseTargetStatus =
  | 'building'
  | 'ready'
  | 'active'
  | 'failed'
  | 'rolled_back'
  | 'cleanup_pending'
  | 'cleaned'

export interface ProductionReleaseTarget {
  id: string
  release_id: string
  document_id: string
  version_id: string
  target_knowledge_base_id: string
  status: ProductionReleaseTargetStatus
  failure_code?: string
  failure_reason?: string
  created_at?: ProductionTimestamp
  updated_at?: ProductionTimestamp
}

export interface ProductionRelease {
  id: string
  project_id: string
  document_id: string
  version_id: string
  status: ProductionReleaseTargetStatus
  targets?: ProductionReleaseTarget[]
}

export function listProductionProjects() {
  return get<ProductionResponse<ProductionProject[]>>('/api/v1/production/projects')
}

export function createProductionProject(command: ProductionCommand<CreateProductionProjectInput>) {
  return post<ProductionResponse<ProductionProject>>('/api/v1/production/projects', command.payload, productionCommandConfig(command))
}

export function assignProductionProjectRole(projectId: string, command: ProductionCommand<AssignProductionProjectRoleInput>) {
  return post<ProductionResponse<void>>(`/api/v1/production/projects/${projectId}/members`, command.payload, productionCommandConfig(command))
}

export function removeProductionProjectRole(projectId: string, userId: string, role: ProductionProjectRole, command: ProductionCommand<void>) {
  return del<ProductionResponse<void>>(`/api/v1/production/projects/${projectId}/members/${userId}/${role}`, undefined, productionCommandConfig(command))
}

export function listProductionDocumentTypes() {
  return get<ProductionResponse<ProductionDocumentType[]>>('/api/v1/production/document-types')
}

export function createProductionDocumentType(command: ProductionCommand<CreateProductionDocumentTypeInput>) {
  return post<ProductionResponse<ProductionDocumentType>>('/api/v1/production/document-types', command.payload, productionCommandConfig(command))
}

export function activateProductionDocumentType(id: string, command: ProductionCommand<void>) {
  return put<ProductionResponse<ProductionDocumentType>>(`/api/v1/production/document-types/${id}/activate`, undefined, productionCommandConfig(command))
}

export function createProductionSourceSet(projectId: string, command: ProductionCommand<CreateProductionSourceSetInput>) {
  return post<ProductionResponse<ProductionSourceSet>>(`/api/v1/production/projects/${projectId}/source-sets`, command.payload, productionCommandConfig(command))
}

export function decideProductionSourceItem(id: string, command: ProductionCommand<DecideProductionSourceItemInput>) {
  return put<ProductionResponse<void>>(`/api/v1/production/source-items/${id}/decision`, command.payload, productionCommandConfig(command))
}

export function freezeProductionSourceSet(id: string, command: ProductionCommand<void>) {
  return post<ProductionResponse<void>>(`/api/v1/production/source-sets/${id}/freeze`, undefined, productionCommandConfig(command))
}

export function createProductionDocument(projectId: string, command: ProductionCommand<CreateProductionDocumentInput>) {
  return post<ProductionResponse<ProductionDocument>>(`/api/v1/production/projects/${projectId}/documents`, command.payload, productionCommandConfig(command))
}

export function listProductionDocumentVersions(id: string) {
  return get<ProductionResponse<ProductionDocumentVersion[]>>(`/api/v1/production/documents/${id}/versions`)
}

export function appendProductionVersion(documentId: string, currentVersionId: string, command: ProductionCommand<AppendProductionVersionInput>) {
  const config = productionCommandConfig(command)
  return post<ProductionResponse<ProductionDocumentVersion>>(
    `/api/v1/production/documents/${documentId}/versions`,
    command.payload,
    { ...config, headers: { ...config.headers, 'If-Match': currentVersionId } },
  )
}

export function startProductionDocumentRun(id: string, command: ProductionCommand<StartProductionDocumentRunInput>) {
  return post<ProductionResponse<ProductionRun>>(`/api/v1/production/documents/${id}/runs`, command.payload, productionCommandConfig(command))
}

export function startProductionCollection(id: string, command: ProductionCommand<StartProductionCollectionInput>) {
  return post<ProductionResponse<ProductionRun>>(`/api/v1/production/source-sets/${id}/collect`, command.payload, productionCommandConfig(command))
}

export function getProductionRun(id: string) {
  return get<ProductionResponse<ProductionRun>>(`/api/v1/production/runs/${id}`)
}

export function decideProductionToolCall(id: string, command: ProductionCommand<DecideProductionToolCallInput>) {
  return post<ProductionResponse<ProductionToolCall>>(`/api/v1/production/tool-calls/${id}/decision`, command.payload, productionCommandConfig(command))
}

export function submitProductionReview(documentId: string, command: ProductionCommand<{ version_id: string }>) {
  return post<ProductionResponse<ProductionReview>>(`/api/v1/production/documents/${documentId}/reviews`, command.payload, productionCommandConfig(command))
}

export function getProductionReview(id: string) {
  return get<ProductionResponse<ProductionReview>>(`/api/v1/production/reviews/${id}`)
}

export function decideProductionReviewStep(reviewId: string, stepId: string, command: ProductionCommand<{ decision: 'approve' | 'reject' | 'changes_requested'; comment: string }>) {
  return post<ProductionResponse<void>>(`/api/v1/production/reviews/${reviewId}/steps/${stepId}/decision`, command.payload, productionCommandConfig(command))
}

export function createProductionRelease(documentId: string, command: ProductionCommand<{ version_id: string; target_knowledge_base_ids: string[] }>) {
  return post<ProductionResponse<ProductionRelease>>(`/api/v1/production/documents/${documentId}/releases`, command.payload, productionCommandConfig(command))
}

export function getProductionReleaseTarget(id: string) {
  return get<ProductionResponse<ProductionReleaseTarget>>(`/api/v1/production/release-targets/${id}`)
}

export function activateProductionReleaseTarget(id: string, command: ProductionCommand<{ expected_lock: number }>) {
  return post<ProductionResponse<void>>(`/api/v1/production/release-targets/${id}/activate`, command.payload, productionCommandConfig(command))
}

export function retryProductionReleaseTarget(id: string, command: ProductionCommand<void>) {
  return postEmpty<ProductionResponse<void>>(`/api/v1/production/release-targets/${id}/retry`, productionCommandConfig(command))
}

export function rollbackProductionReleaseTarget(id: string, command: ProductionCommand<{ expected_lock: number }>) {
  return post<ProductionResponse<void>>(`/api/v1/production/release-targets/${id}/rollback`, command.payload, productionCommandConfig(command))
}
