export interface AppendVersionFailure<T> {
  conflict: boolean
  command: T | null
}

export function resolveAppendVersionFailure<T>(cause: unknown, command: T | null): AppendVersionFailure<T> {
  const conflict = typeof cause === 'object'
    && cause !== null
    && 'status' in cause
    && (cause as { status?: unknown }).status === 409

  return { conflict, command: conflict ? null : command }
}
