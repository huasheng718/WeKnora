export interface LatestRequestHandlers<T> {
  success?: (value: T) => void
  error?: (cause: unknown) => void
  settled?: () => void
}

export function createLatestRequestCoordinator() {
  let generation = 0

  return {
    invalidate(): void {
      generation++
    },
    async run<T>(operation: () => Promise<T>, handlers: LatestRequestHandlers<T> = {}): Promise<void> {
      const requestGeneration = ++generation
      try {
        const value = await operation()
        if (requestGeneration === generation) handlers.success?.(value)
      } catch (cause) {
        if (requestGeneration === generation) handlers.error?.(cause)
      } finally {
        if (requestGeneration === generation) handlers.settled?.()
      }
    },
  }
}
