import { request, type Job, type Release, type RequestOptions } from '@/lib/api'
import type {
  MovieFile,
  MovieProfile,
  ReleaseDecision,
  RenameResult,
  RootFolder,
  SyncResult,
  Title,
} from '@/lib/movies-api'

export type { Job, MovieFile, MovieProfile, ReleaseDecision, RenameResult, RootFolder, Release, SyncResult, Title }

const basePath = '/api/v1'

// Series metadata adds the season count the movie Title does not carry.
export type SeriesTitle = Title & { totalSeasons?: number }

export type Series = {
  id: string
  metadata: SeriesTitle
  monitored: boolean
  monitorMode: string
  profileId: string
  rootId: string
  tags: string[] | null
  addedAt: string
  updatedAt: string
  lastRefreshAt: string | null
  error: string
  episodes?: Episode[] | null
  status: string
  downloaded: number
  total: number
  wanted: number
}

export type Episode = {
  id: string
  seriesId: string
  imdbId: string
  title: string
  season: number
  number: number
  airDate: string
  rating: number | null
  monitored: boolean
  files: MovieFile[] | null
  lastSearchAt: string | null
  error: string
  status: string
}

export type Target = { season: number; episode: number }

export type AddSeriesInput = {
  imdbId?: string
  metadata?: SeriesTitle
  monitored: boolean
  monitorMode: string
  profileId: string
  rootId: string
  tags?: string[]
}

export type BulkSeriesInput = {
  ids: string[]
  monitored?: boolean
  monitorMode?: string
  profileId?: string
  rootId?: string
  tags?: string[]
}

export type MonitorInput = {
  season?: number
  episodeIds?: string[]
  monitored: boolean
}

export type ManualEpisodeInput = {
  season: number
  number: number
  title?: string
  airDate?: string
}

export type TvRelease = Release & {
  imdbId?: string
  season?: number
  episode?: number
  decision: ReleaseDecision
  episodeIds?: string[] | null
  pack: boolean
}

export type TvHistoryEntry = {
  id: number
  seriesId: string
  episodeId: string
  type: string
  message: string
  createdAt: string
}

export type CalendarEntry = Episode & {
  seriesTitle: string
  poster: string
}

export type ScanCandidate = {
  path: string
  size: number
  title: string
  year: number
  imdbId: string
  quality: string
  season: number
  episodes: number[] | null
  airDate: string
  matchedSeriesId?: string
  error?: string
}

export type ImportInput = {
  rootId: string
  path: string
  seriesId: string
  season: number
  episodes: number[]
}

export type TvConfig = {
  rootFolders: RootFolder[]
  folderTemplate: string
  fileTemplate: string
  importMode: string
  writeNFO: boolean
  pollMinutes: number
  searchHours: number
  retryFailed: boolean
}

function seriesPath(id: string, suffix = '') {
  return `/tv/${encodeURIComponent(id)}${suffix}`
}

// Go nil slices serialize as null; treat them as empty lists.
async function requestArray<T>(path: string, options: RequestOptions = {}): Promise<T[]> {
  const result = await request<T[] | null>(path, options)
  return Array.isArray(result) ? result : []
}

export const tvApi = {
  list: (signal?: AbortSignal) => requestArray<Series>('/tv', { signal }),
  detail: (id: string, signal?: AbortSignal) => request<Series>(seriesPath(id), { signal }),
  add: (input: AddSeriesInput) => request<Series>('/tv', { method: 'POST', body: input }),
  update: (id: string, series: Series) =>
    request<Series>(seriesPath(id), { method: 'PUT', body: { ...series, episodes: undefined } }),
  remove: (id: string, deleteFiles: boolean) =>
    request<{ ok: boolean }>(`${seriesPath(id)}?deleteFiles=${deleteFiles ? 'true' : 'false'}`, {
      method: 'DELETE',
    }),
  bulk: (input: BulkSeriesInput) => requestArray<Series>('/tv/bulk', { method: 'POST', body: input }),
  discover: (query: string, page = 1, signal?: AbortSignal) =>
    requestArray<SeriesTitle>(`/tv/discover?q=${encodeURIComponent(query)}&page=${page}`, { signal }),
  refresh: (id: string) => request<Series>(seriesPath(id, '/refresh'), { method: 'POST', body: {} }),
  addEpisode: (id: string, input: ManualEpisodeInput) =>
    request<Episode>(seriesPath(id, '/episodes'), { method: 'POST', body: input }),
  monitor: (id: string, input: MonitorInput) =>
    request<Series>(seriesPath(id, '/monitor'), { method: 'POST', body: input }),
  search: (id: string, target: Target, signal?: AbortSignal) =>
    requestArray<TvRelease>(seriesPath(id, '/search'), { method: 'POST', body: target, signal }),
  grab: (id: string, input: Target & { releaseId: string; override: boolean }) =>
    request<Job>(seriesPath(id, '/grab'), { method: 'POST', body: input }),
  rename: (id: string, preview: boolean) =>
    request<RenameResult>(seriesPath(id, '/rename'), { method: 'POST', body: { preview } }),
  history: (id: string, signal?: AbortSignal) =>
    requestArray<TvHistoryEntry>(seriesPath(id, '/history'), { signal }),
  allHistory: (signal?: AbortSignal) => requestArray<TvHistoryEntry>('/tv/history', { signal }),
  calendar: (signal?: AbortSignal) => requestArray<CalendarEntry>('/tv/calendar', { signal }),
  calendarIcsUrl: () => `${basePath}/tv/calendar.ics`,
  fileUrl: (id: string, path: string) => `${basePath}${seriesPath(id, '/file')}?path=${encodeURIComponent(path)}`,
  scan: (rootId: string, signal?: AbortSignal) =>
    requestArray<ScanCandidate>('/tv/scan', { method: 'POST', body: { rootId }, signal }),
  importFiles: (input: ImportInput) => request<Series>('/tv/import', { method: 'POST', body: input }),
  sync: () => request<SyncResult>('/tv/sync', { method: 'POST', body: {} }),
  config: (signal?: AbortSignal) => request<TvConfig>('/tv-config', { signal }),
  saveConfig: (config: TvConfig) => request<TvConfig>('/tv-config', { method: 'PUT', body: config }),
}
