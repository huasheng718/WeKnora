import assert from 'node:assert/strict'
import test from 'node:test'

import { summarizeProject, type ProjectSummaryInput } from './projectSummary'

function fixtureProject(): ProjectSummaryInput {
  return {
    project: {
      id: 'project-1',
      updated_at: '2026-07-20T10:00:00Z',
    },
    documents: [
      { id: 'document-1', project_id: 'project-1', updated_at: '2026-07-20T11:00:00Z' },
      { id: 'document-2', project_id: 'project-2', updated_at: '2026-07-20T12:00:00Z' },
    ],
    sourceSets: [
      { id: 'source-1', project_id: 'project-1', created_at: '2026-07-20T09:00:00Z' },
    ],
    reviews: [
      { id: 'review-1', project_id: 'project-1', status: 'pending', updated_at: '2026-07-20T12:00:00Z' },
      { id: 'review-2', project_id: 'project-1', status: 'changes_requested', updated_at: '2026-07-20T13:00:00Z' },
      { id: 'review-3', project_id: 'project-1', status: 'approved', updated_at: '2026-07-20T14:00:00Z' },
      { id: 'review-4', project_id: 'project-2', status: 'pending', updated_at: '2026-07-20T15:00:00Z' },
    ],
    releaseTargets: [
      { id: 'target-1', project_id: 'project-1', status: 'failed', updated_at: '2026-07-20T16:00:00Z' },
      { id: 'target-2', project_id: 'project-1', status: 'cleanup_pending', updated_at: '2026-07-20T17:00:00Z' },
      { id: 'target-3', project_id: 'project-2', status: 'failed', updated_at: '2026-07-20T18:00:00Z' },
    ],
    runs: [
      { id: 'run-1', project_id: 'project-1', status: 'running', updated_at: '2026-07-20T19:00:00Z' },
      { id: 'run-2', project_id: 'project-1', status: 'completed', updated_at: '2026-07-20T20:00:00Z' },
      { id: 'run-3', project_id: 'project-2', status: 'queued', updated_at: '2026-07-20T21:00:00Z' },
    ],
  }
}

test('project summary counts only actionable review and release rows', () => {
  const summary = summarizeProject(fixtureProject())

  assert.equal(summary.documentCount, 1)
  assert.equal(summary.sourceSetCount, 1)
  assert.equal(summary.pendingReviews, 2)
  assert.equal(summary.failedTargets, 1)
  assert.equal(summary.inFlightRuns, 1)
  assert.equal(summary.latestActivity, '2026-07-20T20:00:00Z')
})

test('project summary falls back to the project timestamp when related rows are empty', () => {
  const input = fixtureProject()
  const summary = summarizeProject({
    ...input,
    documents: [],
    sourceSets: [],
    reviews: [],
    releaseTargets: [],
    runs: [],
  })

  assert.equal(summary.latestActivity, input.project.updated_at)
})
