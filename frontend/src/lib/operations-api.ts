import { ApiError, request, type RequestOptions } from '@/lib/api'
import { unwrapList, type ListEnvelope } from '@/lib/operations-envelope'

export type AlertSeverity = 'info' | 'warning' | 'critical'

export type AlertThresholds = {
  minimumFreeBytes: number
  minimumFreePercent: number
  windowMinutes: number
  threshold: number
  stuckMinutes: number
  failures: number
}

export type AlertRule = {
  name: string
  enabled: boolean
  severity: AlertSeverity
  thresholds: AlertThresholds
  firing: boolean
  value: number
  message?: string
  since?: string
}

// Rules arrive without live state from the configuration endpoint.
export type AlertRuleConfig = Omit<AlertRule, 'firing' | 'value' | 'message' | 'since'>

export type WebhookSettings = {
  enabled: boolean
  url: string
  headerName: string
  secretConfigured: boolean
  timeoutSeconds: number
  minimumIntervalSeconds: number
  notifyRecovery: boolean
}

export type WebhookUpdate = Omit<WebhookSettings, 'secretConfigured'> & { secret?: string }

export type AlertsConfig = { rules: AlertRuleConfig[]; webhook: WebhookSettings }

export type AlertHistoryEntry = {
  id: number
  name: string
  firing: boolean
  severity: AlertSeverity
  message?: string
  value: number
  at: string
}

export type OperationEvent = {
  id: number
  at: string
  kind: string
  severity: AlertSeverity
  source?: string
  message?: string
  ref?: string
}

export type BackupOrigin = 'manual' | 'scheduled' | 'rollback' | 'imported'

export type Backup = {
  id: string
  createdAt: string
  origin: BackupOrigin
  formatVersion: number
  schemaVersion: number
  bytes: number
  databaseBytes: number
  filesBytes: number
  verified: boolean
  serverVersion?: string
  included: string[]
  excluded: string[]
  rollbackFor?: string
  problem?: string
}

export type BackupConfig = { scheduleEnabled: boolean; intervalHours: number; retentionCount: number }

export type RestorePlan = {
  backup: Backup
  liveRestoreEnabled: boolean
  quiesceConfigured: boolean
  checksumVerified: boolean
  schemaCompatible: boolean
  currentSchemaVersion: number
  willCreateRollbackBackup: boolean
  requiresRestart: boolean
  autoRestart: boolean
  warnings?: string[]
}

export type RestoreResult = {
  backupId: string
  rollbackBackupId?: string
  restartRequired: boolean
  autoRestart: boolean
  durationSeconds: number
  notes?: string[]
}

export type OperationsStatus = {
  database: string
  startedAt: string
  storage: { freeBytes: number; totalBytes: number; usedPercent: number; known: boolean }
  downloads: { queued: number; active: number; failed: number; bytesDone: number; bytesTotal: number }
  torrents: { queued: number; active: number; seeding: number; failed: number; bytesDone: number; bytesTotal: number }
  library: { movies: number; series: number; episodes: number; artists: number; albums: number; tracks: number }
  failures: { downloadFailures: number; importErrors: number; providerFailures: number }
  alerts: { firing: number; total: number }
  backups: { scheduleEnabled: boolean; intervalHours: number; retentionCount: number; count: number; lastCreatedAt?: string }
  metricsPath: string
}

// Uploads stay inside the bound the API accepts for base64 JSON; larger archives belong on the server.
export const maxUploadBytes = 64 * 1024 * 1024

// Backups, restores, and imports outlive the default request window the rest of the interface uses.
const operationTimeout = 30 * 60 * 1000
const readTimeout = 15 * 1000

function withTimeout(signal: AbortSignal | undefined, timeoutMs: number) {
  if (typeof AbortSignal.any !== 'function') return signal
  const timeout = AbortSignal.timeout(timeoutMs)
  return signal ? AbortSignal.any([signal, timeout]) : timeout
}

function backupPath(id: string) {
  return `/operations/backups/${encodeURIComponent(id)}`
}

async function importBackup(file: File) {
  if (file.size > maxUploadBytes) {
    throw new ApiError('Backup archives up to 64 MB can be uploaded here. Copy larger archives to the server instead.')
  }
  const bytes = new Uint8Array(await file.arrayBuffer())
  let binary = ''
  for (let index = 0; index < bytes.length; index += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000))
  }
  return request<Backup>('/operations/backups/import', {
    method: 'POST',
    body: { data: btoa(binary) },
    signal: withTimeout(undefined, operationTimeout),
  })
}

// The list routes answer with a named envelope; unwrapping by name fails loudly instead of showing empty lists.
async function requestList<T>(path: string, key: ListEnvelope, options: RequestOptions = {}): Promise<T[]> {
  return unwrapList<T>(await request<unknown>(path, options), key)
}

export const operationsApi = {
  status: (signal?: AbortSignal) => request<OperationsStatus>('/operations/status', { signal: withTimeout(signal, readTimeout) }),
  events: (signal?: AbortSignal, kind?: string) =>
    requestList<OperationEvent>(`/operations/events${kind ? `?kind=${encodeURIComponent(kind)}` : ''}`, 'events', {
      signal: withTimeout(signal, readTimeout),
    }),
  alerts: (signal?: AbortSignal) =>
    request<{ alerts: AlertRule[]; generatedAt: string }>('/operations/alerts', { signal: withTimeout(signal, readTimeout) }),
  alertsConfig: (signal?: AbortSignal) =>
    request<AlertsConfig>('/operations/alerts/config', { signal: withTimeout(signal, readTimeout) }),
  saveAlertsConfig: (body: { rules: AlertRuleConfig[]; webhook: WebhookUpdate }) =>
    request<AlertsConfig>('/operations/alerts/config', { method: 'PUT', body, signal: withTimeout(undefined, readTimeout) }),
  alertHistory: (signal?: AbortSignal, name?: string) =>
    requestList<AlertHistoryEntry>(`/operations/alerts/history${name ? `?name=${encodeURIComponent(name)}` : ''}`, 'events', {
      signal: withTimeout(signal, readTimeout),
    }),
  backups: (signal?: AbortSignal) =>
    requestList<Backup>('/operations/backups', 'backups', { signal: withTimeout(signal, readTimeout) }),
  backup: (id: string, signal?: AbortSignal) => request<Backup>(backupPath(id), { signal: withTimeout(signal, readTimeout) }),
  createBackup: () =>
    request<Backup>('/operations/backups', { method: 'POST', body: {}, signal: withTimeout(undefined, operationTimeout) }),
  deleteBackup: (id: string) =>
    request<{ ok: boolean }>(backupPath(id), { method: 'DELETE', signal: withTimeout(undefined, readTimeout) }),
  downloadUrl: (id: string) => `/api/v1${backupPath(id)}/download`,
  importBackup,
  backupConfig: (signal?: AbortSignal) =>
    request<BackupConfig>('/operations/backups/config', { signal: withTimeout(signal, readTimeout) }),
  saveBackupConfig: (config: BackupConfig) =>
    request<BackupConfig>('/operations/backups/config', { method: 'PUT', body: config, signal: withTimeout(undefined, readTimeout) }),
  previewRestore: (id: string) =>
    request<RestorePlan>(`${backupPath(id)}/restore`, {
      method: 'POST',
      body: { preview: true, confirm: '' },
      signal: withTimeout(undefined, operationTimeout),
    }),
  restore: (id: string) =>
    request<RestoreResult>(`${backupPath(id)}/restore`, {
      method: 'POST',
      body: { preview: false, confirm: id },
      signal: withTimeout(undefined, operationTimeout),
    }),
}
