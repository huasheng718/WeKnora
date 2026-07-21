import { snapshotProductionJSON } from './jsonSnapshot'

export interface ProductionCommand<T> {
  readonly idempotencyKey: string
  readonly payload: T
}

export interface ProductionCommandConfig {
  headers: Record<'Idempotency-Key', string>
}

export function createProductionCommand<T>(
  payload: T,
  createKey: () => string = () => crypto.randomUUID(),
): ProductionCommand<T> {
  return {
    idempotencyKey: createKey(),
    payload: snapshotProductionJSON(payload),
  }
}

export function productionCommandConfig<T>(command: ProductionCommand<T>): ProductionCommandConfig {
  return {
    headers: {
      'Idempotency-Key': command.idempotencyKey,
    },
  }
}
