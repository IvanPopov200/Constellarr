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

export type Release = { id: string; title: string; size: number; published: string }

export type JobStatus =
  | 'queued'
  | 'downloading'
  | 'verifying'
  | 'repairing'
  | 'extracting'
  | 'completed'
  | 'failed'

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

export class ApiError extends Error {}

export function errorMessage(cause: unknown) {
  return cause instanceof Error ? cause.message : 'Something went wrong.'
}

export function isActiveJob(job: Job) {
  return job.status !== 'completed' && job.status !== 'failed'
}

type RequestOptions = {
  method?: 'GET' | 'POST'
  body?: unknown
  signal?: AbortSignal
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, signal } = options
  let response: Response

  try {
    response = await fetch(`${basePath}${path}`, {
      method,
      cache: 'no-store',
      signal,
      headers: {
        Accept: 'application/json',
        ...(method === 'POST' ? { 'Content-Type': 'application/json' } : {}),
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
}
