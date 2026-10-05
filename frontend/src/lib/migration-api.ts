import { request } from '@/lib/api'

export const migrationApps = [
  'radarr',
  'sonarr',
  'lidarr',
  'prowlarr',
  'bazarr',
  'sabnzbd',
  'nzbget',
  'transmission',
  'jellyfin',
] as const

export type MigrationApp = (typeof migrationApps)[number]

export const migrationAppLabels: Record<MigrationApp, string> = {
  radarr: 'Radarr',
  sonarr: 'Sonarr',
  lidarr: 'Lidarr',
  prowlarr: 'Prowlarr',
  bazarr: 'Bazarr',
  sabnzbd: 'SABnzbd',
  nzbget: 'NZBGet',
  transmission: 'Transmission',
  jellyfin: 'Jellyfin',
}

// Credential shape per application; the server decides which fields are required.
export const migrationAppCredentials: Record<MigrationApp, { apiKey: boolean; login: boolean }> = {
  radarr: { apiKey: true, login: false },
  sonarr: { apiKey: true, login: false },
  lidarr: { apiKey: true, login: false },
  prowlarr: { apiKey: true, login: false },
  bazarr: { apiKey: true, login: false },
  sabnzbd: { apiKey: true, login: false },
  nzbget: { apiKey: false, login: true },
  transmission: { apiKey: false, login: true },
  jellyfin: { apiKey: true, login: false },
}

export type MigrationConnection = {
  app: MigrationApp
  url: string
  apiKey?: string
  username?: string
  password?: string
}

export type ConnectionTestResult = { app: MigrationApp; ok: boolean; version?: string; error?: string }
export type ConnectionTest = { results: ConnectionTestResult[] }

export type PlanCounts = {
  movies: number
  series: number
  seasons: number
  files: number
  artists: number
  albums: number
  profiles: number
  roots: number
  indexers: number
  newsServers: number
  torrents: number
  languages: number
}

export type RootPlan = {
  source: MigrationApp
  media: 'movies' | 'tv'
  path: string
  accessible: boolean
  suggestedLocalPath?: string
}

export type ProfilePlan = {
  source: MigrationApp
  key: string
  name: string
  qualities: string[] | null
  cutoff: string
  upgrade: boolean
  suggestedProfileId?: string
  unsupported?: string[]
}

export type NamingPlan = {
  source: MigrationApp
  media: 'movies' | 'tv'
  folder: string
  file: string
  applicable: boolean
  unsupported?: string[]
}

export type IndexerPlan = { key: string; source: MigrationApp; name: string; url: string; apiKeySet: boolean }

export type UsenetPlan = {
  key: string
  source: MigrationApp
  host: string
  port: number
  usernameSet: boolean
  connections: number
  tls: boolean
  notes?: string[] | null
}

export type TorznabPlan = { key: string; source: MigrationApp; name: string; url: string; protocol: string; apiKeySet: boolean }

export type MusicProfilePlan = {
  source: MigrationApp
  key: string
  name: string
  formats: string[] | null
  losslessOnly: boolean
  minBitrateKbps: number
  cutoff: string
  upgrade: boolean
  suggestedProfileId?: string
  unsupported?: string[] | null
}

export type MusicPlanView = {
  roots: RootPlan[] | null
  profiles: MusicProfilePlan[] | null
  naming: NamingPlan[] | null
  counts: PlanCounts
}

export type JellyfinPlan = { version: string; product: string; libraries: Record<string, number>; locations: number }

export type SubtitlePlan = {
  source: MigrationApp
  version: string
  languages: string[] | null
  singleLanguage: boolean
  minimumScore: number
  minimumScoreMovie: number
  upgradeSubtitles: boolean
  searchHours: number
  defaultProfile?: string
  movieProfile?: string
  profiles: { key: string; name: string; languages: { code: string; forced: boolean; hi: boolean }[] | null; cutoff: number }[] | null
  providerPlans: { key: string; name: string; type: string; usernameSet: boolean; passwordSet: boolean; unsupported?: string[] | null }[] | null
  providers: string[] | null
  sync: { enabled: boolean; maxOffsetSeconds: number; threshold: number }
}

export type TorrentPlan = {
  source: MigrationApp
  version: string
  downloadDir: string
  incompleteDir?: string
  speedLimitDown: number
  speedLimitUp: number
  speedDownEnabled: boolean
  speedUpEnabled: boolean
  seedRatioLimit: number
  seedRatioLimited: boolean
  idleSeedingMinutes: number
  idleSeedingLimited: boolean
  dhtEnabled: boolean
  pexEnabled: boolean
  downloadQueueSize: number
  torrents: number
  labels?: string[] | null
  transfers: {
    name: string
    hash: string
    magnetLink?: string
    percentDone: number
    bytesDone: number
    totalSize: number
    downloadDir: string
    files?: { name: string; size: number; done: number }[] | null
  }[] | null
}

export type PlanItemStatus = 'pending' | 'done' | 'failed' | 'skipped'

export type PlanItem = {
  position: number
  kind: string
  target: string
  label: string
  status: PlanItemStatus
  message: string
}

export type ItemTotals = { total: number; pending: number; done: number; failed: number; skipped: number }

export type UnsupportedPlan = { area: string; detail: string }

export type MigrationPlan = {
  id: string
  status: 'ready' | 'applying' | 'applied' | 'partial'
  createdAt: string
  expiresAt: string
  sources: { app: MigrationApp; version: string }[] | null
  counts: PlanCounts
  roots: RootPlan[] | null
  profiles: ProfilePlan[] | null
  naming: NamingPlan[] | null
  indexers: IndexerPlan[] | null
  usenetSources: UsenetPlan[] | null
  torznab: TorznabPlan[] | null
  music?: MusicPlanView
  jellyfin?: JellyfinPlan
  subtitles?: SubtitlePlan
  torrents?: TorrentPlan
  warnings: string[] | null
  unsupported: UnsupportedPlan[] | null
  totals: ItemTotals
  failures: PlanItem[] | null
}

export type MigrationApplyInput = {
  movies: boolean
  tv: boolean
  music: boolean
  files: boolean
  naming: boolean
  providers: boolean
  subtitles: boolean
  torrents: boolean
  torrentJobs: boolean
  createProfiles: boolean
  retryFailed: boolean
  moviesRoots: Record<string, string>
  tvRoots: Record<string, string>
  musicRoots: Record<string, string>
  torrentRoots: Record<string, string>
  profiles: Record<string, string>
  musicProfiles: Record<string, string>
  indexer: string
  usenet: string
  usenetFallbacks: string[]
  torznab: string[]
  redownload: string[]
  connections?: MigrationConnection[]
}

export type MigrationApplyResult = {
  planId: string
  status: 'ready' | 'applying' | 'applied' | 'partial'
  remaining: number
  totals: ItemTotals
  applied: PlanItem[] | null
  failures: PlanItem[] | null
  message?: string
}

export const migrationApi = {
  testConnections: (connections: MigrationConnection[]) =>
    request<ConnectionTest>('/migration/connections/test', { method: 'POST', body: { connections } }),
  preview: (connections: MigrationConnection[]) =>
    request<MigrationPlan>('/migration/preview', { method: 'POST', body: { connections } }),
  getPlan: (id: string, signal?: AbortSignal) =>
    request<MigrationPlan>(`/migration/plans/${encodeURIComponent(id)}`, { signal }),
  apply: (id: string, input: MigrationApplyInput) =>
    request<MigrationApplyResult>(`/migration/plans/${encodeURIComponent(id)}/apply`, { method: 'POST', body: input }),
}
