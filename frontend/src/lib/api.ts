const basePath = '/api/v1'

export type IndexerSource = { name: string; configured: boolean }

export type UsenetSource = {
  name: string
  configured: boolean
  host: string
  port: number
  connections: number
}

export type Sources = { indexer: IndexerSource; usenet: UsenetSource }

export type SourceTestResult = { ok: boolean; error?: string }

export type SourceTest = { indexer: SourceTestResult; usenet: SourceTestResult }

export type Settings = {
  indexer: { url: string; apiKeyConfigured: boolean }
  usenet: {
    host: string
    port: number
    username: string
    passwordConfigured: boolean
    connections: number
    fallbackHosts: string[]
  }
  storage: { directory: string }
}

export type SettingsUpdate = {
  indexer: { url: string; apiKey?: string }
  usenet: Omit<Settings['usenet'], 'passwordConfigured'> & { password?: string }
}

export type Release = {
  id: string; title: string; size: number; published: string
  protocol?: 'usenet' | 'torrent'
  source?: string
  seeders?: number
}

export type JobStatus =
  | 'queued'
  | 'downloading'
  | 'verifying'
  | 'repairing'
  | 'extracting'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled'

export type OutputFile = { name: string; size: number; url: string }

export type Job = {
  id: string
  releaseId: string
  title: string
  status: JobStatus
  bytesDone: number
  bytesTotal: number
  segmentsDone: number
  segmentsTotal: number
  missingSegments: number
  createdAt: string
  updatedAt: string
  error?: string
  files: OutputFile[]
}

export type DownloadLimitMode = 'unlimited' | 'kbps' | 'percent'

export type DownloadLimit = { mode: DownloadLimitMode; value: number }

export type DownloadWindowAction = 'full' | 'limited' | 'paused'

export type DownloadWindow = {
  id: string
  name: string
  days: number[]
  start: string
  end: string
  action: DownloadWindowAction
  limit: DownloadLimit
}

export type DownloadPolicyConfig = {
  paused: boolean
  timezone: string
  connectionMbps: number
  limit: DownloadLimit
  scheduleEnabled: boolean
  outsideSchedule: 'normal' | 'paused'
  windows: DownloadWindow[]
}

export type DownloadPolicyEffective = {
  paused: boolean
  reason: string
  limitBytesPerSecond: number
  nextChange?: string
}

export type DownloadPolicySnapshot = {
  config: DownloadPolicyConfig
  effective: DownloadPolicyEffective
}

export class ApiError extends Error {}

export function errorMessage(cause: unknown) {
  return cause instanceof Error ? cause.message : 'Something went wrong.'
}

export function isActiveJob(job: Job) {
  return job.status !== 'completed' && job.status !== 'failed' && job.status !== 'cancelled'
}

// Transfer limits travel as KiB/s; the interface shows MiB/s and connection speeds in Mbps.
export function mibPerSecondToKib(mibPerSecond: number) {
  return Math.round(mibPerSecond * 1024)
}

export function kibPerSecondToMib(kibPerSecond: number) {
  return kibPerSecond / 1024
}

export function mbpsToKibPerSecond(mbps: number) {
  return Math.round((mbps * 1_000_000) / 8 / 1024)
}

export function likelyTimeZone(value: string) {
  return /^[A-Za-z][A-Za-z0-9_+-]*(?:\/[A-Za-z0-9_+-]+)*$/.test(value.trim())
}

export function formatSpeedLimit(kibPerSecond: number) {
  if (!Number.isFinite(kibPerSecond) || kibPerSecond <= 0) return 'Unlimited'
  const mib = kibPerSecondToMib(kibPerSecond)
  if (mib < 1) return `${Math.round(kibPerSecond)} KiB/s`
  return `${mib >= 10 ? Math.round(mib * 10) / 10 : Number(mib.toFixed(1))} MiB/s`
}

export type RequestOptions = {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  signal?: AbortSignal
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal } = options
  let response: Response

  try {
    response = await fetch(`${basePath}${path}`, {
      method,
      cache: 'no-store',
      signal,
      headers: {
        Accept: 'application/json',
        ...(method !== 'GET' ? { 'Content-Type': 'application/json' } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    throw new ApiError(
      'The backend could not be reached. Confirm the API server is running, then retry.',
    )
  }

  if (!response.ok) {
    if (response.status === 401) window.dispatchEvent(new Event('constellarr:unauthorized'))
    const detail = (await response.json().catch(() => null)) as { error?: unknown } | null
    throw new ApiError(
      typeof detail?.error === 'string'
        ? detail.error
        : `The backend returned HTTP ${response.status}.`,
    )
  }

  return (await response.json()) as T
}

export const api = {
  getSettings: (signal?: AbortSignal) => request<Settings>('/settings', { signal }),
  saveSettings: (body: SettingsUpdate) => request<Settings>('/settings', { method: 'PUT', body }),
  getSources: (signal?: AbortSignal) => request<Sources>('/sources', { signal }),
  testSources: () =>
    request<SourceTest>('/sources/test', {
      method: 'POST',
    }),
  searchReleases: (query: string, signal?: AbortSignal) =>
    request<Release[]>(`/releases?q=${encodeURIComponent(query)}`, { signal }),
  listDownloads: (signal?: AbortSignal) => request<Job[]>('/downloads', { signal }),
  createDownload: (release: Release) =>
    request<Job>('/downloads', {
      method: 'POST',
      body: { releaseId: release.id, title: release.title },
    }),
  retryDownload: (id: string) =>
    request<Job>(`/downloads/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
  pauseDownload: (id: string) =>
    request<Job>(`/downloads/${encodeURIComponent(id)}/pause`, { method: 'POST' }),
  resumeDownload: (id: string) =>
    request<Job>(`/downloads/${encodeURIComponent(id)}/resume`, { method: 'POST' }),
  cancelDownload: (id: string) =>
    request<Job>(`/downloads/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  getDownloadPolicy: (signal?: AbortSignal) =>
    request<DownloadPolicySnapshot>('/downloads/policy', { signal }),
  saveDownloadPolicy: (config: DownloadPolicyConfig) =>
    request<DownloadPolicySnapshot>('/downloads/policy', { method: 'PUT', body: config }),
  pauseAllDownloads: () => request<DownloadPolicySnapshot>('/downloads/pause', { method: 'POST' }),
  resumeAllDownloads: () => request<DownloadPolicySnapshot>('/downloads/resume', { method: 'POST' }),
}
