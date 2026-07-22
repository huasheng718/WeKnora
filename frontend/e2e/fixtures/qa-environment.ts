import { tmpdir } from 'node:os'
import { isAbsolute, relative, resolve } from 'node:path'

export interface ProductionQaEnvironment {
  backendURL: string
  frontendURL: string
  dataDir: string
  mode: 'ephemeral-sqlite'
}

export function validateProductionQaEnvironment(
  environment: Record<string, string | undefined>,
): ProductionQaEnvironment {
  if (environment.PLAYWRIGHT_PRODUCTION_QA !== '1') {
    throw new Error('production QA requires explicit opt-in with PLAYWRIGHT_PRODUCTION_QA=1')
  }
  if (environment.PLAYWRIGHT_QA_BACKEND_MODE !== 'ephemeral-sqlite') {
    throw new Error('production QA requires PLAYWRIGHT_QA_BACKEND_MODE=ephemeral-sqlite')
  }

  const backendURL = parseLoopbackOrigin(environment.PLAYWRIGHT_BACKEND_URL, 'PLAYWRIGHT_BACKEND_URL')
  const frontendPort = parseDedicatedPort(environment.PLAYWRIGHT_PORT ?? '15177', 'PLAYWRIGHT_PORT')
  const frontendURL = parseLoopbackOrigin(
    environment.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${frontendPort}`,
    'PLAYWRIGHT_BASE_URL',
  )
  if (frontendURL.port !== String(frontendPort)) {
    throw new Error('PLAYWRIGHT_BASE_URL must use the port declared by PLAYWRIGHT_PORT')
  }
  const dataDir = validateTemporaryDataDirectory(environment.PLAYWRIGHT_QA_DATA_DIR)

  return {
    backendURL: backendURL.toString().replace(/\/$/, ''),
    frontendURL: frontendURL.toString().replace(/\/$/, ''),
    dataDir,
    mode: 'ephemeral-sqlite',
  }
}

function parseLoopbackOrigin(value: string | undefined, variable: string): URL {
  let parsed: URL
  try {
    parsed = new URL(value ?? '')
  } catch {
    throw new Error(`${variable} must be an HTTP loopback URL`)
  }
  if (parsed.protocol !== 'http:' || parsed.hostname !== '127.0.0.1') {
    throw new Error(`${variable} must use HTTP on the 127.0.0.1 loopback address`)
  }
  parseDedicatedPort(parsed.port, variable)
  if (parsed.username || parsed.password || parsed.pathname !== '/' || parsed.search || parsed.hash) {
    throw new Error(`${variable} must contain only the loopback origin and dedicated port`)
  }
  return parsed
}

function parseDedicatedPort(value: string, variable: string): number {
  const port = Number(value)
  if (!Number.isInteger(port) || port < 10_000 || port > 65_535) {
    throw new Error(`${variable} must use a dedicated port from 10000 to 65535`)
  }
  return port
}

function validateTemporaryDataDirectory(value: string | undefined): string {
  if (!value || !isAbsolute(value)) {
    throw new Error('PLAYWRIGHT_QA_DATA_DIR must be an absolute temporary directory')
  }
  const temporaryRoot = resolve(tmpdir())
  const dataDir = resolve(value)
  const pathFromTemporaryRoot = relative(temporaryRoot, dataDir)
  if (!pathFromTemporaryRoot || pathFromTemporaryRoot.startsWith('..') || isAbsolute(pathFromTemporaryRoot)) {
    throw new Error('PLAYWRIGHT_QA_DATA_DIR must be a child of the operating system temporary directory')
  }
  return dataDir
}
