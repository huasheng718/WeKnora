export interface ScopedMutation {
  generation: number
  scopeId: string
}

export function createScopedMutationCoordinator() {
  let generation = 0

  return {
    start(scopeId: string): ScopedMutation {
      return { generation: ++generation, scopeId }
    },
    invalidate(): void {
      generation++
    },
    isCurrent(mutation: ScopedMutation, scopeId: string): boolean {
      return mutation.generation === generation && mutation.scopeId === scopeId
    },
  }
}
