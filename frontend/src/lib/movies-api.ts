import { request, type RequestOptions, type Job } from '@/lib/api'

const basePath = '/api/v1'

export type RootFolder = { id: string; path: string }

export type MovieConfig = {
  rootFolders: RootFolder[]
  folderTemplate: string
  fileTemplate: string
  importMode: string
  writeNFO: boolean
  pollMinutes: number
  searchHours: number
  minimumAvailability: string
  retryFailed: boolean
  metadataURL: string
  metadataAPIKey?: string
  metadataConfigured: boolean
  jellyfinURL: string
  jellyfinAPIKey?: string
  jellyfinConfigured: boolean
  webhookURL: string
}

export type Title = {
  imdbId: string
  title: string
  year: number
  type: string
  released: string
  rating: number | null
  votes: number
  runtime: number
  directors: string[] | null
  cast: string[] | null
  genres: string[] | null
  languages: string[] | null
  countries: string[] | null
  certification: string
  poster: string
  plot: string
}

export type MovieFile = {
  rootId: string
  path: string
  size: number
  quality: string
  score: number
  importedAt: string
}

export type Movie = {
  id: string
  metadata: Title
  monitored: boolean
  profileId: string
  rootId: string
  tags: string[] | null
  collection: string
  files: MovieFile[] | null
  status: string
  addedAt: string
  updatedAt: string
  lastSearchAt: string | null
  error: string
}

export type HistoryEntry = {
  id: number
  movieId: string
  type: string
  message: string
  createdAt: string
}

export type QualityRule = {
  name: string
  pattern: string
  score: number
  required: boolean
  negate: boolean
}

export type MovieProfile = {
  id: string
  name: string
  qualities: string[] | null
  cutoff: string
  upgrade: boolean
  minMB: number
  maxMB: number
  language: string
  minScore: number
  cutoffScore: number
  rules: QualityRule[] | null
}

export type ReleaseDetails = {
  quality: string
  resolution: number
  source: string
  codec: string
  audio: string
  hdr: string
  language: string
  group: string
  edition: string
  proper: boolean
}

export type ReleaseDecision = {
  details: ReleaseDetails
  score: number
  rank: number
  allowed: boolean
  upgrade: boolean
  reasons: string[] | null
}

export type MovieRelease = {
  id: string
  title: string
  size: number
  published: string
  imdbId?: string
  decision: ReleaseDecision
}

export type Watchlist = {
  id: string
  name: string
  url: string
  imdbIds: string[] | null
  monitor: boolean
  profileId: string
  rootId: string
  intervalHours: number
  lastSyncAt: string | null
  error: string
}

export type ScanCandidate = {
  path: string
  size: number
  title: string
  year: number
  imdbId: string
  quality: string
  matchedMovieId?: string
  error?: string
}

export type SyncResult = { searched: number; queued: number; imported: number }

export type ConfigTest = {
  metadata: { ok: boolean; error?: string }
  jellyfin: { ok: boolean; error?: string }
}

export type RenameResult = { files: { from: string; to: string }[]; applied: boolean }

export type AddMovieInput = {
  imdbId?: string
  metadata?: Title
  monitored: boolean
  profileId: string
  rootId: string
  tags?: string[]
  collection?: string
}

export type BulkEditInput = {
  ids: string[]
  monitored?: boolean
  profileId?: string
  rootId?: string
  tags?: string[]
  collection?: string
}

function moviePath(id: string, suffix = '') {
  return `/movies/${encodeURIComponent(id)}${suffix}`
}

// Go nil slices serialize as null; treat them as empty lists.
async function requestArray<T>(path: string, options: RequestOptions = {}): Promise<T[]> {
  const result = await request<T[] | null>(path, options)
  return Array.isArray(result) ? result : []
}

export const moviesApi = {
  list: (signal?: AbortSignal) => requestArray<Movie>('/movies', { signal }),
  discover: (query: string, page = 1, signal?: AbortSignal) =>
    requestArray<Title>(`/movies/discover?q=${encodeURIComponent(query)}&page=${page}`, { signal }),
  add: (input: AddMovieInput) => request<Movie>('/movies', { method: 'POST', body: input }),
  update: (movie: Movie) => request<Movie>(moviePath(movie.id), { method: 'PUT', body: movie }),
  remove: (id: string) => request<{ ok: boolean }>(moviePath(id), { method: 'DELETE' }),
  bulkEdit: (input: BulkEditInput) => requestArray<Movie>('/movies/bulk', { method: 'POST', body: input }),
  searchReleases: (id: string, signal?: AbortSignal) =>
    requestArray<MovieRelease>(moviePath(id, '/search'), { method: 'POST', body: {}, signal }),
  grab: (id: string, releaseId: string, override = false) =>
    request<Job>(moviePath(id, '/grab'), { method: 'POST', body: { releaseId, override } }),
  refresh: (id: string) => request<Movie>(moviePath(id, '/refresh'), { method: 'POST', body: {} }),
  rename: (id: string, preview: boolean) =>
    request<RenameResult>(moviePath(id, '/rename'), { method: 'POST', body: { preview } }),
  history: (id: string, signal?: AbortSignal) =>
    requestArray<HistoryEntry>(moviePath(id, '/history'), { signal }),
  allHistory: (signal?: AbortSignal) => requestArray<HistoryEntry>('/movies/history', { signal }),
  fileUrl: (id: string, path: string) =>
    `${basePath}${moviePath(id, '/file')}?path=${encodeURIComponent(path)}`,
  calendar: (signal?: AbortSignal) => requestArray<Movie>('/movies/calendar', { signal }),
  calendarIcsUrl: () => `${basePath}/movies/calendar.ics`,
  scan: (rootId: string, signal?: AbortSignal) =>
    requestArray<ScanCandidate>('/movies/scan', { method: 'POST', body: { rootId }, signal }),
  importFile: (input: { rootId: string; path: string; movieId?: string; imdbId?: string }) =>
    request<Movie>('/movies/import', { method: 'POST', body: input }),
  sync: () => request<SyncResult>('/movies/sync', { method: 'POST', body: {} }),
  profiles: (signal?: AbortSignal) => requestArray<MovieProfile>('/movie-profiles', { signal }),
  saveProfile: (profile: MovieProfile) =>
    request<MovieProfile>('/movie-profiles', { method: 'PUT', body: profile }),
  deleteProfile: (id: string) =>
    request<{ ok: boolean }>(`/movie-profiles/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  config: (signal?: AbortSignal) => request<MovieConfig>('/movie-config', { signal }),
  saveConfig: (config: MovieConfig) => request<MovieConfig>('/movie-config', { method: 'PUT', body: config }),
  testConfig: () => request<ConfigTest>('/movie-config/test', { method: 'POST', body: {} }),
  watchlists: (signal?: AbortSignal) => requestArray<Watchlist>('/movie-watchlists', { signal }),
  saveWatchlist: (watchlist: Watchlist) =>
    request<Watchlist>('/movie-watchlists', { method: 'PUT', body: watchlist }),
  deleteWatchlist: (id: string) =>
    request<{ ok: boolean }>(`/movie-watchlists/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  syncWatchlist: (id: string) =>
    request<{ added: number }>(`/movie-watchlists/${encodeURIComponent(id)}/sync`, {
      method: 'POST',
      body: {},
    }),
}
