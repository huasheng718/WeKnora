export interface ProductionCommand<T> {
  idempotencyKey: string
  payload: T
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
    payload,
  }
}

export function productionCommandConfig<T>(command: ProductionCommand<T>): ProductionCommandConfig {
  return {
    headers: {
      'Idempotency-Key': command.idempotencyKey,
    },
  }
}
