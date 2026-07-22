import { del, get, post, postEmpty, put } from '@/utils/request'
import {
  type ProductionCommand,
  productionCommandConfig,
} from './idempotency'

export type ProductionJSON = null | boolean | number | string | ProductionJSON[] | { [key: string]: ProductionJSON }
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
  deleted_at: ProductionTimestamp | null
  current_user_roles?: ProductionProjectRole[]
  summary?: ProductionProjectSummary
}

export interface ProductionProjectSummary {
  document_count: number
  source_set_count: number
  pending_reviews: number
  failed_targets: number
  in_flight_runs: number
  latest_activity: ProductionTimestamp
}

export interface ProductionProjectMember {
  project_id: string
  user_id: string
  role: ProductionProjectRole
  assigned_by: string
  created_at: ProductionTimestamp
  deleted_at: ProductionTimestamp | null
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
  deleted_at: ProductionTimestamp | null
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

export interface ProductionEvidenceSnapshot {
  id: string
  source_item_id: string
  snapshot_type: 'text' | 'json' | 'file' | 'tool_result'
  storage_path?: string
  inline_content?: ProductionJSON
  content_digest: string
  redaction_metadata: ProductionJSON
  captured_by_run_id?: string
  captured_by_tool_call_id?: string
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
  content: ProductionJSON
  attributes: ProductionJSON
  evidence_refs: ProductionJSON
  ai_provenance: ProductionJSON
  content_digest: string
}

export interface ProductionBlockLineage {
  id: string
  from_version_id: string
  from_logical_block_id: string
  to_version_id: string
  to_logical_block_id: string
  relation: 'same' | 'split' | 'merged'
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
  blocks: ProductionDocumentBlock[]
  lineage?: ProductionBlockLineage[]
  document_type_code?: string
}

export type ProductionDocumentVersionSummary = Omit<ProductionDocumentVersion, 'blocks' | 'lineage'>
export type ProductionDocumentVersionDetail = Omit<ProductionDocumentVersion, 'lineage'> & { lineage: ProductionBlockLineage[] }

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
    content?: ProductionJSON
    attributes?: ProductionJSON
    evidence_refs?: ProductionJSON
    ai_provenance?: ProductionJSON
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
  wakeup_version: number
  wakeup_enqueued_version: number
  state_payload: ProductionJSON
  model_id: string
  document_type_snapshot: ProductionJSON
  workflow_plan_snapshot: ProductionJSON
  workflow_plan_digest: string
  input_version_id?: string
  output_version_id?: string
  idempotency_key: string
  raw_model_response?: ProductionJSON
  raw_model_response_digest?: string
  error_code?: string
  error_message?: string
  started_at?: ProductionTimestamp
  completed_at?: ProductionTimestamp
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface ProductionToolCall {
  id: string
  run_id: string
  tenant_id: number
  project_id: string
  document_id?: string
  source_set_id: string
  attempt: number
  current_step: number
  idempotency_key: string
  status: 'planned' | 'pending_approval' | 'approved' | 'rejected' | 'executing' | 'completed' | 'failed'
  approval_status: 'not_required' | 'pending' | 'approved' | 'rejected'
  provider_type: 'skill' | 'mcp' | 'datasource'
  provider_id: string
  tool_name: string
  request_snapshot: ProductionJSON
  request_digest: string
  response_snapshot?: ProductionJSON
  response_digest?: string
  response_evidence_id?: string
  response_evidence_source_item_id?: string
  approval_requested_at?: ProductionTimestamp
  approved_by?: string
  approved_at?: ProductionTimestamp
  rejected_by?: string
  rejected_at?: ProductionTimestamp
  error_code?: string
  error_message?: string
  started_at?: ProductionTimestamp
  completed_at?: ProductionTimestamp
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
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
  tenant_id: number
  project_id: string
  document_id: string
  version_id: string
  block_id: string
  annotation_type: 'comment' | 'suggestion' | 'quality_tag'
  quality_tag?: 'missing_evidence' | 'factual_risk' | 'unclear' | 'incomplete' | 'conflict' | 'compliance_risk'
  severity: 'info' | 'warning' | 'blocking'
  anchor: ProductionJSON
  status: 'open' | 'resolved' | 'dismissed'
  body: string
  suggested_content?: string
  created_by: string
  resolved_by?: string
  resolved_at?: ProductionTimestamp
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface CreateProductionAnnotationInput {
  version_id: string
  block_id: string
  annotation_type: ProductionAnnotation['annotation_type']
  quality_tag?: NonNullable<ProductionAnnotation['quality_tag']>
  severity: ProductionAnnotation['severity']
  anchor: ProductionJSON
  body: string
  suggested_content?: string
}

export interface ListProductionAnnotationsQuery {
  version_id?: string
  status?: ProductionAnnotation['status']
  annotation_type?: ProductionAnnotation['annotation_type']
  severity?: ProductionAnnotation['severity']
  page?: number
  page_size?: number
}

export interface ProductionAnnotationListResponse extends ProductionResponse<ProductionAnnotation[]> {
  total: number
  page: number
  page_size: number
}

export interface ProductionReview {
  id: string
  tenant_id: number
  project_id: string
  document_id: string
  version_id: string
  policy_snapshot: ProductionJSON
  policy_digest: string
  status: 'pending' | 'approved' | 'rejected' | 'obsolete' | 'cancelled' | 'changes_requested'
  submitted_by: string
  submitted_at: ProductionTimestamp
  terminal_by?: string
  terminal_reason?: string
  completed_at?: ProductionTimestamp
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
  steps?: ProductionReviewStep[]
}

export interface ProductionReviewStep {
  id: string
  review_request_id: string
  tenant_id: number
  project_id: string
  document_id: string
  version_id: string
  required_role: ProductionProjectRole
  sequence: number
  reviewer_user_id?: string
  decision: 'pending' | 'approved' | 'rejected' | 'cancelled' | 'changes_requested'
  comment: string
  decided_at?: ProductionTimestamp
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface DecideProductionReviewStepInput {
  decision: Extract<ProductionReviewStep['decision'], 'approved' | 'rejected' | 'changes_requested'>
  comment: string
}

export type ProductionReleaseStatus =
  | 'building'
  | 'ready'
  | 'active'
  | 'failed'
  | 'rolled_back'
  | 'cleanup_pending'
  | 'cleaned'

export type ProductionReleaseTargetStatus = ProductionReleaseStatus

export interface ProductionReleaseTarget {
  id: string
  release_id: string
  tenant_id: number
  project_id: string
  document_id: string
  version_id: string
  target_knowledge_base_id: string
  knowledge_id: string
  release_digest: string
  config_snapshot: ProductionJSON
  config_digest: string
  status: ProductionReleaseTargetStatus
  failure_code?: string
  failure_reason?: string
  recovery_attempted_at?: ProductionTimestamp
  retention_days: number
  retention_until?: ProductionTimestamp
  activated_at?: ProductionTimestamp
  failed_at?: ProductionTimestamp
  rolled_back_at?: ProductionTimestamp
  cleanup_requested_at?: ProductionTimestamp
  cleaned_at?: ProductionTimestamp
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
}

export interface ProductionRelease {
  id: string
  tenant_id: number
  project_id: string
  document_id: string
  version_id: string
  review_request_id: string
  release_digest: string
  release_digest_version: number
  supersedes_release_id?: string
  status: ProductionReleaseStatus
  retention_days: number
  created_by: string
  created_at: ProductionTimestamp
  updated_at: ProductionTimestamp
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

export function listProductionSourceSets(projectId: string) {
  return get<ProductionResponse<ProductionSourceSet[]>>(`/api/v1/production/projects/${projectId}/source-sets`)
}

export function decideProductionSourceItem(id: string, command: ProductionCommand<DecideProductionSourceItemInput>) {
  return put<ProductionResponse<void>>(`/api/v1/production/source-items/${id}/decision`, command.payload, productionCommandConfig(command))
}

export function freezeProductionSourceSet(id: string, command: ProductionCommand<void>) {
  return post<ProductionResponse<void>>(`/api/v1/production/source-sets/${id}/freeze`, undefined, productionCommandConfig(command))
}

export function listProductionEvidence(sourceSetId: string) {
  return get<ProductionResponse<ProductionEvidenceSnapshot[]>>(`/api/v1/production/source-sets/${sourceSetId}/evidence`)
}

export function createProductionDocument(projectId: string, command: ProductionCommand<CreateProductionDocumentInput>) {
  return post<ProductionResponse<ProductionDocument>>(`/api/v1/production/projects/${projectId}/documents`, command.payload, productionCommandConfig(command))
}

export function listProductionDocuments(projectId: string) {
  return get<ProductionResponse<ProductionDocument[]>>(`/api/v1/production/projects/${projectId}/documents`)
}

export function getProductionDocument(id: string) {
  return get<ProductionResponse<ProductionDocument>>(`/api/v1/production/documents/${id}`)
}

export function listProductionDocumentVersions(id: string) {
  return get<ProductionResponse<ProductionDocumentVersionSummary[]>>(`/api/v1/production/documents/${id}/versions`)
}

export function getProductionDocumentVersion(documentId: string, versionId: string) {
  return get<ProductionResponse<ProductionDocumentVersionDetail>>(`/api/v1/production/documents/${documentId}/versions/${versionId}`)
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

export function listProductionDocumentRuns(documentId: string) {
  return get<ProductionResponse<ProductionRun[]>>(`/api/v1/production/documents/${documentId}/runs`)
}

export function listProductionRunToolCalls(runId: string) {
  return get<ProductionResponse<ProductionToolCall[]>>(`/api/v1/production/runs/${runId}/tool-calls`)
}

export function decideProductionToolCall(id: string, command: ProductionCommand<DecideProductionToolCallInput>) {
  return post<ProductionResponse<ProductionToolCall>>(`/api/v1/production/tool-calls/${id}/decision`, command.payload, productionCommandConfig(command))
}

export function createProductionAnnotation(documentId: string, command: ProductionCommand<CreateProductionAnnotationInput>) {
  return post<ProductionResponse<ProductionAnnotation>>(`/api/v1/production/documents/${documentId}/annotations`, command.payload, productionCommandConfig(command))
}

export function listProductionAnnotations(documentId: string, query: ListProductionAnnotationsQuery = {}) {
  return get<ProductionAnnotationListResponse>(`/api/v1/production/documents/${documentId}/annotations`, { params: query })
}

export function updateProductionAnnotationStatus(id: string, command: ProductionCommand<{ status: Extract<ProductionAnnotation['status'], 'resolved' | 'dismissed'> }>) {
  return put<ProductionResponse<void>>(`/api/v1/production/annotations/${id}/status`, command.payload, productionCommandConfig(command))
}

export function submitProductionReview(documentId: string, command: ProductionCommand<{ version_id: string }>) {
  return post<ProductionResponse<ProductionReview>>(`/api/v1/production/documents/${documentId}/reviews`, command.payload, productionCommandConfig(command))
}

export function getProductionReview(id: string) {
  return get<ProductionResponse<ProductionReview>>(`/api/v1/production/reviews/${id}`)
}

export function decideProductionReviewStep(reviewId: string, stepId: string, command: ProductionCommand<DecideProductionReviewStepInput>) {
  return post<ProductionResponse<void>>(`/api/v1/production/reviews/${reviewId}/steps/${stepId}/decision`, command.payload, productionCommandConfig(command))
}

export function rejectProductionReview(id: string, command: ProductionCommand<{ reason: string }>) {
  return post<ProductionResponse<void>>(`/api/v1/production/reviews/${id}/reject`, command.payload, productionCommandConfig(command))
}

export function cancelProductionReview(id: string, command: ProductionCommand<{ reason: string }>) {
  return post<ProductionResponse<void>>(`/api/v1/production/reviews/${id}/cancel`, command.payload, productionCommandConfig(command))
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
