import { request, type Job, type RequestOptions } from '@/lib/api'

const basePath = '/api/v1'

export type RootFolder = { id: string; path: string }

export type QualityProfile = {
  id: string
  name: string
  formats: string[]
  losslessOnly: boolean
  minBitrateKbps: number
  minMB: number
  maxMB: number
  cutoff: string
  upgrade: boolean
}

export type MusicConfig = {
  rootFolders: RootFolder[]
  folderTemplate: string
  fileTemplate: string
  importMode: string
  ffprobePath: string
  musicBrainzURL: string
  musicBrainzRateMs: number
  coverArtURL: string
  pollMinutes: number
  searchHours: number
  retryFailed: boolean
  qualityProfiles: QualityProfile[]
  defaultProfileId: string
  musicBrainzReady: boolean
  ffprobeAvailable: boolean
}

export type TrackFile = {
  rootId: string
  path: string
  size: number
  format: string
  bitrateKbps: number
  lossless: boolean
  score: number
  importedAt: string
  missing?: boolean
}

export type Track = {
  id: string
  musicBrainzId: string
  disc: number
  number: number
  title: string
  artist: string
  durationMs: number
  file?: TrackFile
  missing?: boolean
}

export type AlbumFile = {
  rootId: string
  path: string
  size: number
  format: string
  bitrateKbps: number
  lossless: boolean
  score: number
  disc: number
  number: number
  trackTitle: string
  importedAt: string
  missing?: boolean
}

export type Album = {
  id: string
  artistId: string
  artistName: string
  musicBrainzId: string
  title: string
  year: number
  releaseDate: string
  type: string
  monitored: boolean
  profileId: string
  rootId: string
  tracks: Track[] | null
  files: AlbumFile[] | null
  format: string
  score: number
  size: number
  coverPath: string
  status: string
  error: string
  addedAt: string
  updatedAt: string
  lastSearchAt: string | null
}

export type Artist = {
  id: string
  musicBrainzId: string
  name: string
  sortName: string
  disambiguation: string
  country: string
  type: string
  monitored: boolean
  monitorOption: string
  profileId: string
  rootId: string
  albums?: Album[] | null
  status: string
  error: string
  addedAt: string
  updatedAt: string
  lastSearchAt: string | null
  lastRefreshAt: string | null
}

export type ReleaseDecision = {
  format: string
  bitrateKbps: number
  lossless: boolean
  media: string
  score: number
  rank: number
  allowed: boolean
  upgrade: boolean
  reasons: string[] | null
}

export type MusicRelease = {
  id: string
  protocol?: string
  source?: string
  seeders?: number
  title: string
  size: number
  published: string
  artist: string
  album: string
  year: number
  decision: ReleaseDecision
  error?: string
}

export type HistoryEntry = {
  id: number
  albumId: string
  artistId: string
  type: string
  message: string
  createdAt: string
}

export type Candidate = {
  path: string
  size: number
  artist: string
  album: string
  year: number
  discs: number
  tracks: number
  format: string
  bitrateKbps: number
  matchedAlbumId?: string
  error?: string
}

export type ArtistResult = {
  musicBrainzId: string
  name: string
  sortName: string
  disambiguation: string
  country: string
  type: string
  score: number
  inLibrary?: boolean
}

export type AlbumResult = {
  musicBrainzId: string
  title: string
  artistName: string
  artistId: string
  year: number
  type: string
  score: number
  inLibrary?: boolean
}

export type DiscoverResult = { artists: ArtistResult[] | null; albums: AlbumResult[] | null }

export type MusicSyncResult = { searched: number; queued: number; imported: number; refreshed: number }

export type RenameResult = { files: { from: string; to: string }[]; applied: boolean }

export type MusicConnectionTests = {
  musicBrainz: { ok: boolean; error?: string }
  indexer: { ok: boolean; error?: string }
  ffprobe: { ok: boolean; error?: string }
}

export type AddArtistInput = {
  musicBrainzId?: string
  name?: string
  monitored: boolean
  monitorOption: string
  profileId: string
  rootId: string
}

export type AddAlbumInput = {
  artistId: string
  musicBrainzId?: string
  title: string
  year?: number
  monitored: boolean
  profileId: string
  rootId: string
}

// Go nil slices serialize as null; treat them as empty lists.
async function requestArray<T>(path: string, options: RequestOptions = {}): Promise<T[]> {
  const result = await request<T[] | null>(path, options)
  return Array.isArray(result) ? result : []
}

function albumPath(id: string, suffix = '') {
  return `/music/albums/${encodeURIComponent(id)}${suffix}`
}

export const musicApi = {
  config: (signal?: AbortSignal) => request<MusicConfig>('/music/config', { signal }),
  saveConfig: (config: MusicConfig) => request<MusicConfig>('/music/config', { method: 'PUT', body: config }),
  testConfig: () => request<MusicConnectionTests>('/music/config/test', { method: 'POST', body: {} }),

  artists: (signal?: AbortSignal) => requestArray<Artist>('/music/artists', { signal }),
  artist: (id: string, signal?: AbortSignal) => request<Artist>(`/music/artists/${encodeURIComponent(id)}`, { signal }),
  addArtist: (input: AddArtistInput) => request<Artist>('/music/artists', { method: 'POST', body: input }),
  updateArtist: (artist: Artist) =>
    request<Artist>(`/music/artists/${encodeURIComponent(artist.id)}`, { method: 'PUT', body: artist }),
  removeArtist: (id: string, deleteFiles = false) =>
    request<{ ok: boolean }>(`/music/artists/${encodeURIComponent(id)}?deleteFiles=${deleteFiles}`, { method: 'DELETE' }),
  monitorArtist: (id: string, monitored: boolean, monitorOption: string) =>
    request<Artist>(`/music/artists/${encodeURIComponent(id)}/monitor`, { method: 'POST', body: { monitored, monitorOption } }),
  refreshArtist: (id: string) => request<Artist>(`/music/artists/${encodeURIComponent(id)}/refresh`, { method: 'POST', body: {} }),
  artistHistory: (id: string, signal?: AbortSignal) =>
    requestArray<HistoryEntry>(`/music/artists/${encodeURIComponent(id)}/history`, { signal }),

  albums: (signal?: AbortSignal) => requestArray<Album>('/music/albums', { signal }),
  album: (id: string, signal?: AbortSignal) => request<Album>(albumPath(id), { signal }),
  addAlbum: (input: AddAlbumInput) => request<Album>('/music/albums', { method: 'POST', body: input }),
  updateAlbum: (album: Album) => request<Album>(albumPath(album.id), { method: 'PUT', body: album }),
  removeAlbum: (id: string, deleteFiles = false) =>
    request<{ ok: boolean }>(`${albumPath(id)}?deleteFiles=${deleteFiles}`, { method: 'DELETE' }),
  monitorAlbum: (id: string, monitored: boolean, profileId: string) =>
    request<Album>(albumPath(id, '/monitor'), { method: 'POST', body: { monitored, profileId } }),
  searchAlbum: (id: string, signal?: AbortSignal) => requestArray<MusicRelease>(albumPath(id, '/search'), { method: 'POST', body: {}, signal }),
  grab: (id: string, releaseId: string, override = false) =>
    request<Job>(albumPath(id, '/grab'), { method: 'POST', body: { releaseId, override } }),
  refreshAlbum: (id: string) => request<Album>(albumPath(id, '/refresh'), { method: 'POST', body: {} }),
  rename: (id: string, preview: boolean) => request<RenameResult>(albumPath(id, '/rename'), { method: 'POST', body: { preview } }),
  albumHistory: (id: string, signal?: AbortSignal) => requestArray<HistoryEntry>(albumPath(id, '/history'), { signal }),
  coverUrl: (id: string) => `${basePath}${albumPath(id, '/cover')}`,

  releaseSearch: (query: string, albumId = '', signal?: AbortSignal) =>
    requestArray<MusicRelease>(`/music/search?q=${encodeURIComponent(query)}&albumId=${encodeURIComponent(albumId)}`, { signal }),
  discover: (query: string, page = 1, signal?: AbortSignal) =>
    request<DiscoverResult>(`/music/discover?q=${encodeURIComponent(query)}&page=${page}`, { signal }),
  wanted: (signal?: AbortSignal) => requestArray<Album>('/music/wanted', { signal }),
  calendar: (signal?: AbortSignal) => requestArray<{ id: string; albumId: string; artistId: string; title: string; artistName: string; releaseDate: string; type: string }>('/music/calendar', { signal }),
  history: (signal?: AbortSignal) => requestArray<HistoryEntry>('/music/history', { signal }),
  scan: (rootId: string, signal?: AbortSignal) =>
    requestArray<Candidate>('/music/scan', { method: 'POST', body: { rootId }, signal }),
  importPath: (input: { rootId?: string; path: string; albumId?: string }) =>
    request<Album>('/music/import', { method: 'POST', body: input }),
  sync: (force = false) => request<MusicSyncResult>('/music/sync', { method: 'POST', body: { force } }),
}
