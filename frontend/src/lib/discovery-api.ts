import { request } from '@/lib/api'
import { moviesApi, type MovieProfile, type RootFolder } from '@/lib/movies-api'
import { tvApi } from '@/lib/tv-api'

const basePath = '/api/v1'

export type MediaType = 'movie' | 'tv' | 'music'

export type Permissions = { approve: boolean; requestsWrite: boolean; libraryWrite: boolean }

export type RequestStatus = 'pending' | 'approving' | 'approved' | 'rejected' | 'cancelled' | 'available'

export type Delivery = {
  phase: string
  available: boolean
  progress?: number
  done?: number
  total?: number
  jobId?: string
  message?: string
  attempts?: number
}

export type MediaRequest = {
  id: string
  userId: string
  userName: string
  mediaType: MediaType
  provider: string
  providerId: string
  title: string
  year: number
  poster: string
  status: RequestStatus
  message: string
  decisionNote: string
  decidedBy: string
  decidedByName: string
  decidedAt: string | null
  libraryId: string
  delivery: Delivery
  createdAt: string
  updatedAt: string
}

export type RequestEvent = {
  id: number
  requestId: string
  actor: string
  actorName: string
  action: string
  fromStatus: string
  toStatus: string
  message: string
  createdAt: string
}

export type RequestComment = {
  id: number
  requestId: string
  userId: string
  userName: string
  body: string
  createdAt: string
}

export type RequestList = {
  requests: MediaRequest[] | null
  types: MediaType[] | null
  canApprove: boolean
  permissions: Permissions | null
}

export type RequestDetail = {
  request: MediaRequest
  comments: RequestComment[] | null
  events: RequestEvent[] | null
  canApprove: boolean
  permissions: Permissions | null
}

export type CreateRequestInput = {
  mediaType: MediaType
  providerId: string
  title: string
  year: number
  poster?: string
  message?: string
}

export type ApproveInput = {
  profileId?: string
  rootId?: string
  monitored?: boolean
  monitorMode?: string
  note?: string
}

export type DiscoverResult = {
  mediaType: MediaType
  provider: string
  providerId: string
  title: string
  year: number
  poster: string
}

export type DiscoverResponse = { results: DiscoverResult[] | null; types: MediaType[] | null }

export type CalendarSourceKind = { id: string; mediaType: MediaType; available: boolean }

export type CalendarEntry = {
  id: string
  mediaType: MediaType
  source: string
  title: string
  subtitle: string
  date: string
  year: number
  poster: string
  libraryId: string
  status: string
  imdbId?: string
  season?: number
  episode?: number
  artist?: string
}

export type CalendarView = {
  entries: CalendarEntry[] | null
  sources: CalendarSourceKind[] | null
  from: string
  to: string
}

export type AISettingsView = {
  baseURL: string
  apiKey?: string
  model: string
  maxTokens: number
  temperature: number
  apiKeyConfigured: boolean
}

export type AITestResult = { ok: boolean; error?: string; models?: string[] }

export type RecommendationCandidate = {
  title: string
  mediaType: MediaType
  year: number
  reason: string
  provider: string
  providerId: string
  poster: string
  verified: boolean
  verification: string
  accepted: boolean
}

export type Recommendation = {
  id: string
  userId: string
  model: string
  mediaType: string
  input: { mediaType: string; useHistory: boolean; titles: string[] | null; genres: string[] | null; count: number }
  candidates: RecommendationCandidate[] | null
  warnings: string[] | null
  acceptedAt: string | null
  acceptedAction: string
  acceptedId: string
  createdAt: string
}

export type RecommendationList = {
  recommendations: Recommendation[] | null
  types: MediaType[] | null
  canApprove: boolean
  permissions: Permissions | null
}

export type RecommendationInput = {
  mediaType: string
  useHistory: boolean
  titles: string[]
  genres: string[]
  count: number
}

export type AcceptInput = {
  candidate: number
  action: 'request' | 'add'
  message?: string
  profileId?: string
  rootId?: string
  monitored?: boolean
  monitorMode?: string
}

export type AcceptResult = {
  action: string
  request?: MediaRequest
  libraryId?: string
  recommendation: Recommendation
}

export type CalendarQuery = {
  from?: string
  to?: string
  types?: MediaType[]
  sources?: string[]
}

function listValue<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : []
}

function permissionValue(value: Permissions | null | undefined): Permissions {
  return {
    approve: Boolean(value?.approve),
    requestsWrite: Boolean(value?.requestsWrite),
    libraryWrite: Boolean(value?.libraryWrite),
  }
}

function calendarParams(query: CalendarQuery = {}) {
  const params = new URLSearchParams()
  if (query.from) params.set('from', query.from)
  if (query.to) params.set('to', query.to)
  if (query.types?.length) params.set('types', query.types.join(','))
  if (query.sources?.length) params.set('sources', query.sources.join(','))
  const encoded = params.toString()
  return encoded ? `?${encoded}` : ''
}

export const discoveryApi = {
  listRequests: async (signal?: AbortSignal): Promise<RequestList> => {
    const result = await request<RequestList>('/requests', { signal })
    return {
      requests: listValue(result?.requests),
      types: listValue(result?.types),
      canApprove: Boolean(result?.canApprove),
      permissions: permissionValue(result?.permissions),
    }
  },
  request: (id: string, signal?: AbortSignal) =>
    request<RequestDetail>(`/requests/${encodeURIComponent(id)}`, { signal }).then((detail) => ({
      ...detail,
      comments: listValue(detail.comments),
      events: listValue(detail.events),
      permissions: permissionValue(detail.permissions),
    })),
  createRequest: (input: CreateRequestInput) =>
    request<MediaRequest>('/requests', { method: 'POST', body: input }),
  cancelRequest: (id: string) =>
    request<MediaRequest>(`/requests/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: {} }),
  approveRequest: (id: string, input: ApproveInput) =>
    request<MediaRequest>(`/requests/${encodeURIComponent(id)}/approve`, { method: 'POST', body: input }),
  rejectRequest: (id: string, reason: string) =>
    request<MediaRequest>(`/requests/${encodeURIComponent(id)}/reject`, { method: 'POST', body: { reason } }),
  comment: (id: string, body: string) =>
    request<RequestComment>(`/requests/${encodeURIComponent(id)}/comments`, { method: 'POST', body: { body } }),
  discover: async (mediaType: MediaType, query: string, page = 1, signal?: AbortSignal): Promise<DiscoverResponse> => {
    const result = await request<DiscoverResponse>(
      `/requests/discover?type=${encodeURIComponent(mediaType)}&q=${encodeURIComponent(query)}&page=${page}`,
      { signal },
    )
    return { results: listValue(result?.results), types: listValue(result?.types) }
  },
  calendar: (query: CalendarQuery = {}, signal?: AbortSignal) =>
    request<CalendarView>(`/calendar${calendarParams(query)}`, { signal }).then((view) => ({
      ...view,
      entries: listValue(view.entries),
      sources: listValue(view.sources),
    })),
  calendarIcsUrl: (query: CalendarQuery = {}) => `${basePath}/calendar.ics${calendarParams(query)}`,
  aiSettings: (signal?: AbortSignal) => request<AISettingsView>('/ai/config', { signal }),
  saveAISettings: (settings: AISettingsView) => request<AISettingsView>('/ai/config', { method: 'PUT', body: settings }),
  testAI: () => request<AITestResult>('/ai/test', { method: 'POST', body: {} }),
  aiModels: async (signal?: AbortSignal) => {
    const result = await request<{ models: string[] | null }>('/ai/models', { signal })
    return listValue(result?.models)
  },
  recommendations: async (signal?: AbortSignal): Promise<RecommendationList> => {
    const result = await request<RecommendationList>('/recommendations', { signal })
    return {
      recommendations: listValue(result?.recommendations),
      types: listValue(result?.types),
      canApprove: Boolean(result?.canApprove),
      permissions: permissionValue(result?.permissions),
    }
  },
  generate: (input: RecommendationInput) => request<Recommendation>('/recommendations', { method: 'POST', body: input }),
  accept: (id: string, input: AcceptInput) =>
    request<AcceptResult>(`/recommendations/${encodeURIComponent(id)}/accept`, { method: 'POST', body: input }),
}

// Approvers pick a profile and root from the movie and TV configuration, which stay owned by those pages.
export type ApproverOptions = { profiles: MovieProfile[]; movieRoots: RootFolder[]; tvRoots: RootFolder[] }

export async function approverOptions(signal?: AbortSignal): Promise<ApproverOptions> {
  const [profiles, movieConfig, tvConfig] = await Promise.all([
    moviesApi.profiles(signal).catch(() => []),
    moviesApi.config(signal).catch(() => null),
    tvApi.config(signal).catch(() => null),
  ])
  return {
    profiles,
    movieRoots: movieConfig?.rootFolders ?? [],
    tvRoots: tvConfig?.rootFolders ?? [],
  }
}

export function mediaTypeLabel(type: string) {
  return type === 'movie' ? 'Movie' : type === 'tv' ? 'TV' : type === 'music' ? 'Music' : type
}

export function requestStatusLabel(status: string) {
  switch (status) {
    case 'pending':
      return 'Pending review'
    case 'approving':
      return 'Approving'
    case 'approved':
      return 'Approved'
    case 'available':
      return 'Available'
    case 'rejected':
      return 'Rejected'
    case 'cancelled':
      return 'Cancelled'
    default:
      return status
  }
}

export function deliveryPhaseLabel(phase: string) {
  switch (phase) {
    case 'searching':
      return 'Waiting for a release'
    case 'downloading':
      return 'Downloading'
    case 'paused':
      return 'Download paused'
    case 'cancelled':
      return 'Download cancelled'
    case 'importing':
      return 'Importing'
    case 'failed':
      return 'Download or import failed'
    case 'available':
      return 'In the library'
    case 'unmonitored':
      return 'Not monitored'
    case 'removed':
      return 'Removed from the library'
    default:
      return ''
  }
}

export function catalogHref(mediaType: string, libraryId: string) {
  if (!libraryId) return ''
  if (mediaType === 'movie') return '#movies'
  if (mediaType === 'tv') return '#tv-shows'
  return '#music'
}

export function catalogLabel(mediaType: string) {
  if (mediaType === 'movie') return 'Open Movies'
  if (mediaType === 'tv') return 'Open TV Shows'
  return 'Open Music'
}

// displayName keeps internal account IDs out of the interface and marks the caller as You.
export function displayName(id: string, name: string, currentUserId: string | null, fallback: string) {
  if (!id) return ''
  if (currentUserId && id === currentUserId) return 'You'
  return name || fallback
}

export function relativeAge(value: string) {
  const elapsed = Date.now() - Date.parse(value)
  if (!Number.isFinite(elapsed)) return ''
  const minutes = Math.max(0, Math.floor(elapsed / 60_000))
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return `${Math.floor(days / 30)}mo ago`
}

export function formatDay(value: string) {
  const parsed = new Date(`${value}T00:00:00`)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' })
}

export function isoDay(date: Date) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}
