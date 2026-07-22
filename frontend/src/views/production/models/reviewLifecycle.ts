export interface ReviewVersionCommand<T> {
  versionId: string
  command: T
}

export function commandForReviewVersion<T>(
  versionId: string,
  current: ReviewVersionCommand<T> | null,
  create: () => T,
): ReviewVersionCommand<T> {
  return current?.versionId === versionId ? current : { versionId, command: create() }
}
