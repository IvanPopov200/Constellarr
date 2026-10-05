import { request } from '@/lib/api'

const basePath = '/api/v1'

export type SubtitleJob = {
  id: string
  kind: string
  videoKind: string
  videoId: string
  language: string
  status: string
  progress: number
  detail: string
  error: string
  createdAt: string
  updatedAt: string
}

export type SubtitleLanguagePreference = { code: string; forced: boolean; hi: boolean }

export type SubtitleProfile = {
  id: string
  name: string
  languages: SubtitleLanguagePreference[]
  cutoff: number
}

export type SubtitleProvider = {
  id: string
  name: string
  type: string
  endpoint: string
  username: string
  password?: string
  apiKey?: string
  enabled: boolean
  passwordSet?: boolean
  apiKeySet?: boolean
}

export type SubtitleSyncConfig = {
  helperPath: string
  ffmpegPath: string
  timeoutSeconds: number
  maxOffsetSeconds: number
  minScore: number
  qualityMaxOffsetSeconds: number
  maxFramerateDeviation: number
  vad: string
  audioReferenceSeconds: number
  maxEmbeddedStreamIndex: number
}

export type SubtitleAIConfig = {
  enabled: boolean
  baseURL: string
  apiKey?: string
  apiKeySet?: boolean
  model: string
  timeoutSeconds: number
  maxTokens: number
  maxRequests: number
  maxTotalTokens: number
  maxCharacters: number
  temperature: number
  overrideExisting: boolean
}

export type SubtitleConfig = {
  enabled: boolean
  autoSearch: boolean
  autoDownload: boolean
  scanMinutes: number
  searchIntervalHours: number
  retryMinutes: number
  cutoffScore: number
  providerTimeoutSeconds: number
  defaultProfileId: string
  providers: SubtitleProvider[]
  sync: SubtitleSyncConfig
  ai: SubtitleAIConfig
}

export type SubtitleProviderStatus = {
  providerId: string
  name: string
  type: string
  configured: boolean
  enabled: boolean
  lastError?: string
  quotaRemaining?: number
  quotaReset?: string
}

export type SubtitleProviderTest = {
  providerId: string
  name: string
  ok: boolean
  error?: string
  message?: string
}

export type SubtitleVideo = {
  kind: string
  id: string
  title: string
  seriesTitle?: string
  year: number
  imdbId: string
  seriesImdbId?: string
  season?: number
  episode?: number
  rootId: string
  path: string
  size: number
}

export type SubtitleSidecar = {
  kind: string
  videoId: string
  path: string
  language: string
  format: string
  forced: boolean
  hi: boolean
  source: string
  size: number
  updatedAt: string
}

export type SubtitleWanted = {
  kind: string
  videoId: string
  language: string
  forced: boolean
  hi: boolean
  status: string
  attempts: number
  lastAttemptAt: string | null
  nextAttemptAt: string | null
  error: string
}

export type SubtitleOutput = {
  id: string
  jobId: string
  videoKind: string
  videoId: string
  language: string
  forced: boolean
  hi: boolean
  format: string
  originPath: string
  targetPath: string
  payload?: string
  status: string
  detail: string
  createdAt: string
  appliedAt: string | null
}

export type SubtitleLibraryItem = {
  video: SubtitleVideo
  sidecars: SubtitleSidecar[]
  wanted: SubtitleWanted[]
  outputs: SubtitleOutput[]
  profileId: string
  monitored: boolean
  missing: number
}

export type SubtitleWantedItem = { video: SubtitleVideo; wanted: SubtitleWanted }

export type SubtitleHistoryEntry = {
  id: number
  kind: string
  videoId: string
  action: string
  language: string
  message: string
  createdAt: string
}

export type SubtitleResult = {
  providerId: string
  providerName: string
  fileId: string
  subtitleId: string
  language: string
  format: string
  forced: boolean
  hi: boolean
  fps: number
  downloads: number
  rating: number
  release: string
  fileName: string
  matchedBy: string
  score: number
}

export type SubtitleSearchOutcome = {
  results: SubtitleResult[] | null
  warnings: string[] | null
}

export type SubtitleStream = {
  index: number
  type: string
  codec: string
  language: string
  title: string
  forced: boolean
  hi: boolean
  default: boolean
  channels?: number
}

export type SubtitleDetail = {
  video: SubtitleVideo
  sidecars: SubtitleSidecar[]
  wanted: SubtitleWanted[]
  outputs: SubtitleOutput[]
  history: SubtitleHistoryEntry[]
  profileId: string
  monitored: boolean
}

export type SubtitleScanSummary = { videos: number; sidecars: number; wanted: number }

export type SubtitleDownloadInput = {
  providerId: string
  fileId: string
  language: string
  forced: boolean
  hi: boolean
  fileName?: string
}

export type SubtitleSyncInput = {
  path: string
  mode: 'offset' | 'fps' | 'audio' | 'reference'
  offsetSeconds?: number
  fpsFrom?: number
  fpsTo?: number
  referencePath?: string
  audioStream?: number
  maxOffsetSeconds?: number
  minScore?: number
  noFixFramerate?: boolean
  goldenSectionSearch?: boolean
  vad?: string
  preview?: boolean
}

export type SubtitleTranslateInput = { path: string; language: string; sourceLanguage?: string }
export type SubtitleExtractInput = {
  streamIndex: number
  language?: string
  preview?: boolean
  forced?: boolean
  hi?: boolean
}

export function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'wanted':
      return 'default'
    case 'failed':
      return 'destructive'
    case 'satisfied':
    case 'done':
      return 'secondary'
    default:
      return 'outline'
  }
}

export function timeLabel(value: string | null | undefined) {
  if (!value) return '—'
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) ? new Date(parsed).toLocaleString() : '—'
}

function videoPath(kind: string, id: string, suffix = '') {
  return `/subtitles/${encodeURIComponent(kind)}/${encodeURIComponent(id)}${suffix}`
}

function list<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : []
}

function normalizeProfile(profile: SubtitleProfile): SubtitleProfile {
  return { ...profile, languages: list(profile.languages) }
}

function normalizeConfig(config: SubtitleConfig): SubtitleConfig {
  return { ...config, providers: list(config.providers) }
}

function normalizeDetail(detail: SubtitleDetail): SubtitleDetail {
  return {
    ...detail,
    sidecars: list(detail.sidecars),
    wanted: list(detail.wanted),
    outputs: list(detail.outputs),
    history: list(detail.history),
  }
}

export const subtitlesApi = {
  library: async (params: { q?: string; kind?: string; status?: string; limit?: number }, signal?: AbortSignal) => {
    const query = new URLSearchParams()
    if (params.q) query.set('q', params.q)
    if (params.kind) query.set('kind', params.kind)
    if (params.status) query.set('status', params.status)
    if (params.limit) query.set('limit', String(params.limit))
    const qs = query.toString()
    const items = await request<SubtitleLibraryItem[] | null>(`/subtitles${qs ? `?${qs}` : ''}`, { signal })
    return list(items).map((item) => ({
      ...item,
      sidecars: list(item.sidecars),
      wanted: list(item.wanted),
      outputs: list(item.outputs),
    }))
  },
  wanted: async (signal?: AbortSignal) => list(await request<SubtitleWantedItem[] | null>('/subtitles/wanted', { signal })),
  detail: async (kind: string, id: string, signal?: AbortSignal) =>
    normalizeDetail(await request<SubtitleDetail>(videoPath(kind, id), { signal })),
  scan: () => request<{ started: boolean }>('/subtitles/scan', { method: 'POST', body: {} }),
  jobs: async (activeOnly: boolean, signal?: AbortSignal) =>
    list(await request<SubtitleJob[] | null>(`/subtitles/jobs${activeOnly ? '?active=1' : ''}`, { signal })),
  job: (id: string, signal?: AbortSignal) => request<SubtitleJob>(`/subtitles/jobs/${encodeURIComponent(id)}`, { signal }),
  cancelJob: (id: string) =>
    request<{ ok: boolean }>(`/subtitles/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: {} }),
  history: (params: { kind?: string; id?: string; limit?: number }, signal?: AbortSignal) => {
    const query = new URLSearchParams()
    if (params.kind) query.set('kind', params.kind)
    if (params.id) query.set('id', params.id)
    query.set('limit', String(params.limit ?? 100))
    return request<SubtitleHistoryEntry[] | null>(`/subtitles/history?${query}`, { signal }).then(list)
  },
  providers: async (signal?: AbortSignal) =>
    list(await request<SubtitleProviderStatus[] | null>('/subtitles/providers', { signal })),
  testProviders: async () =>
    list(await request<SubtitleProviderTest[] | null>('/subtitles/providers/test', { method: 'POST', body: {} })),
  config: async (signal?: AbortSignal) => normalizeConfig(await request<SubtitleConfig>('/subtitle-config', { signal })),
  saveConfig: async (config: SubtitleConfig) =>
    normalizeConfig(await request<SubtitleConfig>('/subtitle-config', { method: 'PUT', body: config })),
  profiles: async (signal?: AbortSignal) =>
    list(await request<SubtitleProfile[] | null>('/subtitle-profiles', { signal })).map(normalizeProfile),
  saveProfile: async (profile: SubtitleProfile) =>
    normalizeProfile(await request<SubtitleProfile>('/subtitle-profiles', { method: 'PUT', body: profile })),
  deleteProfile: (id: string) =>
    request<{ ok: boolean }>(`/subtitle-profiles/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  search: async (kind: string, id: string, languages: SubtitleLanguagePreference[], signal?: AbortSignal) => {
    const outcome = await request<SubtitleSearchOutcome>(videoPath(kind, id, '/search'), {
      method: 'POST',
      body: { languages },
      signal,
    })
    return { results: list(outcome.results), warnings: list(outcome.warnings) }
  },
  download: (kind: string, id: string, input: SubtitleDownloadInput) =>
    request<SubtitleJob>(videoPath(kind, id, '/download'), { method: 'POST', body: input }),
  sync: (kind: string, id: string, input: SubtitleSyncInput) =>
    request<SubtitleJob>(videoPath(kind, id, '/sync'), { method: 'POST', body: input }),
  translate: (kind: string, id: string, input: SubtitleTranslateInput) =>
    request<SubtitleJob>(videoPath(kind, id, '/translate'), { method: 'POST', body: input }),
  extract: (kind: string, id: string, input: SubtitleExtractInput) =>
    request<SubtitleJob>(videoPath(kind, id, '/extract'), { method: 'POST', body: input }),
  streams: async (kind: string, id: string, signal?: AbortSignal) =>
    list(await request<SubtitleStream[] | null>(videoPath(kind, id, '/streams'), { signal })),
  setAssignment: (kind: string, id: string, profileId: string, monitored: boolean) =>
    request<{ kind: string; videoId: string; profileId: string; monitored: boolean }>(videoPath(kind, id, '/assignment'), {
      method: 'POST',
      body: { profileId, monitored },
    }),
  resetAssignment: (kind: string, id: string) =>
    request<{ ok: boolean }>(videoPath(kind, id, '/assignment'), { method: 'DELETE' }),
  output: (id: string, signal?: AbortSignal) => request<SubtitleOutput>(`/subtitles/outputs/${encodeURIComponent(id)}`, { signal }),
  applyOutput: (id: string) =>
    request<SubtitleSidecar>(`/subtitles/outputs/${encodeURIComponent(id)}/apply`, { method: 'POST', body: {} }),
  discardOutput: (id: string) =>
    request<{ ok: boolean }>(`/subtitles/outputs/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  fileUrl: (kind: string, id: string, path: string) =>
    `${basePath}${videoPath(kind, id, '/file')}?path=${encodeURIComponent(path)}`,
}
