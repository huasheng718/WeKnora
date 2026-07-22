import { rm } from 'node:fs/promises'
import { validateProductionQaEnvironment } from './fixtures/qa-environment'

export default async function teardownProductionQaEnvironment() {
  const qa = validateProductionQaEnvironment(process.env)
  await rm(qa.dataDir, { recursive: true, force: true })
}
