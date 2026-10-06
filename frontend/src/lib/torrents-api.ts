import { request } from '@/lib/api'

export type TorrentStatus =
  | 'queued'
  | 'metadata'
  | 'checking'
  | 'downloading'
  | 'seeding'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled'

export type TorrentProcessingState = 'pending' | 'running' | 'completed' | 'failed' | 'skipped'

export type TorrentProcessing = { state: TorrentProcessingState; error?: string }

export type TorrentFile = { name: string; size: number; done: number; url?: string }

export type TorrentJob = {
  id: string
  infoHash: string
  name: string
  title?: string
  releaseId?: string
  source: 'magnet' | 'file' | 'torznab'
  status: TorrentStatus
  private: boolean
  bytesDone: number
  bytesTotal: number
  uploaded: number
  downloadRate: number
  uploadRate: number
  ratio: number
  peers: number
  seeds: number
  piecesDone: number
  piecesTotal: number
  etaSeconds: number
  seedRatioLimit: number
  seedTimeLimitMinutes: number
  seedingElapsedSeconds: number
  addedAt: string
  updatedAt: string
  completedAt?: string
  error?: string
  files: TorrentFile[]
  processing?: TorrentProcessing
}

export type TorrentPeer = {
  address: string
  client?: string
  direction: 'incoming' | 'outgoing'
  progress: number
}

export type TorrentDetail = { job: TorrentJob; peers: TorrentPeer[] }

export type TorrentSearchResult = {
  id: string
  sourceId: string
  source: string
  title: string
  size: number
  seeders: number
  leechers: number
  published?: string
  category?: string
  magnet?: string
}

export type TorrentSearchResponse = {
  results: TorrentSearchResult[]
  errors?: { source: string; error: string }[]
}

export type TorrentSource = {
  id: string
  name: string
  url: string
  categories: number[]
  enabled: boolean
  apiKeyConfigured: boolean
  caps?: string[]
}

export type TorrentSourceInput = {
  name: string
  url: string
  apiKey?: string
  categories: number[]
  enabled: boolean
}

export type TorrentSourceTest = { ok: boolean; caps: string[]; error?: string }

export type TorrentSettings = {
  directory: string
  listenPort: number
  dhtEnabled: boolean
  pexEnabled: boolean
  maxActiveJobs: number
  downloadLimitKBps: number
  uploadLimitKBps: number
  seedRatioLimit: number
  seedTimeLimitMinutes: number
  restartRequired?: boolean
}

export type TorrentSettingsUpdate = {
  listenPort: number
  dhtEnabled: boolean
  pexEnabled: boolean
  maxActiveJobs: number
  downloadLimitKBps: number
  uploadLimitKBps: number
  seedRatioLimit: number
  seedTimeLimitMinutes: number
}

export type TorrentHealth = {
  ok: boolean
  started: boolean
  listenPort: number
  dhtEnabled: boolean
  pexEnabled: boolean
  activeJobs: number
  queuedJobs: number
  failedJobs: number
  processingJobs: number
  error?: string
}

export type TorrentAddInput =
  | { magnet: string }
  | { torrent: string; filename?: string }
  | { sourceId: string; resultId: string }

export function isActiveTorrent(job: TorrentJob) {
  return (
    job.status !== 'completed' &&
    job.status !== 'failed' &&
    job.status !== 'paused' &&
    job.status !== 'cancelled'
  )
}

export const torrentsApi = {
  list: (signal?: AbortSignal) => request<{ jobs: TorrentJob[] }>('/torrents', { signal }).then((data) => data.jobs),
  detail: (id: string, signal?: AbortSignal) =>
    request<TorrentDetail>(`/torrents/${encodeURIComponent(id)}`, { signal }),
  add: (input: TorrentAddInput) => request<TorrentJob>('/torrents', { method: 'POST', body: input }),
  pause: (id: string) => request<TorrentJob>(`/torrents/${encodeURIComponent(id)}/pause`, { method: 'POST' }),
  resume: (id: string) => request<TorrentJob>(`/torrents/${encodeURIComponent(id)}/resume`, { method: 'POST' }),
  recheck: (id: string) => request<TorrentJob>(`/torrents/${encodeURIComponent(id)}/recheck`, { method: 'POST' }),
  setLimits: (id: string, seedRatioLimit: number, seedTimeLimitMinutes: number) =>
    request<TorrentJob>(`/torrents/${encodeURIComponent(id)}/limits`, {
      method: 'PUT',
      body: { seedRatioLimit, seedTimeLimitMinutes },
    }),
  remove: (id: string, files: boolean) =>
    request<{ id: string; filesRemoved: boolean }>(
      `/torrents/${encodeURIComponent(id)}?files=${files ? 'true' : 'false'}`,
      { method: 'DELETE' },
    ),
  search: (query: string, sourceId: string, signal?: AbortSignal) => {
    const params = new URLSearchParams({ q: query })
    if (sourceId) params.set('source', sourceId)
    return request<TorrentSearchResponse>(`/torrents/search?${params.toString()}`, { signal })
  },
  listSources: (signal?: AbortSignal) =>
    request<{ sources: TorrentSource[] }>('/torrent-sources', { signal }).then((data) => data.sources),
  createSource: (input: TorrentSourceInput) => request<TorrentSource>('/torrent-sources', { method: 'POST', body: input }),
  updateSource: (id: string, input: TorrentSourceInput) =>
    request<TorrentSource>(`/torrent-sources/${encodeURIComponent(id)}`, { method: 'PUT', body: input }),
  deleteSource: (id: string) =>
    request<{ id: string }>(`/torrent-sources/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  testSource: (id: string) =>
    request<TorrentSourceTest>(`/torrent-sources/${encodeURIComponent(id)}/test`, { method: 'POST' }),
  settings: (signal?: AbortSignal) => request<TorrentSettings>('/torrents/settings', { signal }),
  saveSettings: (input: TorrentSettingsUpdate) =>
    request<TorrentSettings>('/torrents/settings', { method: 'PUT', body: input }),
  health: (signal?: AbortSignal) => request<TorrentHealth>('/torrents/health', { signal }),
}
