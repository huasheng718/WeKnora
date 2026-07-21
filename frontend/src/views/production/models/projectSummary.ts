import type {
  ProductionDocument,
  ProductionProject,
  ProductionReleaseTarget,
  ProductionReview,
  ProductionRun,
  ProductionSourceSet,
} from '@/api/production'

type ProjectRow = Pick<ProductionProject, 'id' | 'updated_at'>
type DocumentRow = Pick<ProductionDocument, 'id' | 'project_id' | 'updated_at'>
type SourceSetRow = Pick<ProductionSourceSet, 'id' | 'project_id' | 'created_at'>
type ReviewRow = Pick<ProductionReview, 'id' | 'project_id' | 'status' | 'updated_at'>
type ReleaseTargetRow = Pick<ProductionReleaseTarget, 'id' | 'project_id' | 'status' | 'updated_at'>
type RunRow = Pick<ProductionRun, 'id' | 'project_id' | 'status' | 'updated_at'>

export interface ProjectSummaryInput {
  project: ProjectRow
  documents: readonly DocumentRow[]
  sourceSets: readonly SourceSetRow[]
  reviews: readonly ReviewRow[]
  releaseTargets: readonly ReleaseTargetRow[]
  runs: readonly RunRow[]
}

export interface ProjectSummary {
  documentCount: number
  sourceSetCount: number
  pendingReviews: number
  failedTargets: number
  inFlightRuns: number
  latestActivity: string
}

const ACTIONABLE_REVIEW_STATUSES = new Set<ProductionReview['status']>(['pending', 'changes_requested'])
const IN_FLIGHT_RUN_STATUSES = new Set<ProductionRun['status']>(['queued', 'running', 'waiting_approval'])

function laterTimestamp(current: string, candidate: string): string {
  return Date.parse(candidate) > Date.parse(current) ? candidate : current
}

export function summarizeProject(input: ProjectSummaryInput): ProjectSummary {
  const { project } = input
  const documents = input.documents.filter(row => row.project_id === project.id)
  const sourceSets = input.sourceSets.filter(row => row.project_id === project.id)
  const reviews = input.reviews.filter(row => row.project_id === project.id)
  const releaseTargets = input.releaseTargets.filter(row => row.project_id === project.id)
  const runs = input.runs.filter(row => row.project_id === project.id)

  const activityTimestamps = [
    ...documents.map(row => row.updated_at),
    ...sourceSets.map(row => row.created_at),
    ...reviews.map(row => row.updated_at),
    ...releaseTargets.map(row => row.updated_at),
    ...runs.map(row => row.updated_at),
  ]

  return {
    documentCount: documents.length,
    sourceSetCount: sourceSets.length,
    pendingReviews: reviews.filter(row => ACTIONABLE_REVIEW_STATUSES.has(row.status)).length,
    failedTargets: releaseTargets.filter(row => row.status === 'failed').length,
    inFlightRuns: runs.filter(row => IN_FLIGHT_RUN_STATUSES.has(row.status)).length,
    latestActivity: activityTimestamps.reduce(laterTimestamp, project.updated_at),
  }
}
