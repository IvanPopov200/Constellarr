import { useCallback, useEffect, useId, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import type { ComponentProps, KeyboardEvent, ReactNode } from 'react'
import { Dialog } from 'radix-ui'
import {
  ArrowDownIcon,
  ArrowUpIcon,
  CalendarIcon,
  CheckIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CircleAlertIcon,
  DownloadIcon,
  FileDownIcon,
  FilmIcon,
  FolderSearchIcon,
  LayoutGridIcon,
  ListFilterIcon,
  ListIcon,
  LoaderCircleIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RefreshCwIcon,
  SaveIcon,
  SearchIcon,
  StarIcon,
  Trash2Icon,
  XIcon,
} from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import {
  moviesApi,
  type AddMovieInput,
  type BulkEditInput,
  type HistoryEntry,
  type Movie,
  type MovieConfig,
  type MovieFile,
  type MovieProfile,
  type MovieRelease,
  type QualityRule,
  type RenameResult,
  type RootFolder,
  type ScanCandidate,
  type SyncResult,
  type Title,
  type Watchlist,
} from '@/lib/movies-api'
import { cn } from 'cn'

const activeStatuses = new Set(['searching', 'downloading', 'importing'])
const watchedIntervalMs = 5000

const statusLabels: Record<string, string> = {
  available: 'Downloaded',
  downloaded: 'Downloaded',
  importing: 'Importing',
  missing: 'Missing',
  wanted: 'Wanted',
  searching: 'Searching',
  downloading: 'Downloading',
  upgrading: 'Upgrading',
  'cutoff-unmet': 'Cutoff unmet',
  failed: 'Failed',
  unmonitored: 'Unmonitored',
}

const statusTones: Record<string, string> = {
  available: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300',
  downloaded: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300',
  importing: 'border-primary/30 bg-primary/10 text-primary',
  searching: 'border-primary/30 bg-primary/10 text-primary',
  downloading: 'border-primary/30 bg-primary/10 text-primary',
  upgrading: 'border-primary/30 bg-primary/10 text-primary',
  missing: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  wanted: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  'cutoff-unmet': 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  unmonitored: 'border-border bg-muted/60 text-muted-foreground',
}

function notifyMoviesChanged() {
  window.dispatchEvent(new Event('movies-changed'))
}

function moviesRouteActive() {
  const hash = window.location.hash.replace(/^#\/?/, '')
  if (!hash) return true
  return hash === 'movies' || hash === 'search'
}

function subscribeToRoute(onChange: () => void) {
  window.addEventListener('hashchange', onChange)
  window.addEventListener('popstate', onChange)
  return () => {
    window.removeEventListener('hashchange', onChange)
    window.removeEventListener('popstate', onChange)
  }
}

// Radix portals escape the hidden route wrapper, so dialogs open only on the movies route.
function useMoviesRoute() {
  return useSyncExternalStore(subscribeToRoute, moviesRouteActive, () => true)
}

function strings(value: string[] | null | undefined) {
  return (value ?? []).filter((item) => item.trim().length > 0)
}

function first(value: string[] | null | undefined) {
  return strings(value)[0] ?? null
}

function splitList(value: string) {
  return value
    .split(/[,\n]/)
    .map((item) => item.trim())
    .filter(Boolean)
}

function stateKey(movie: Movie) {
  return movieState(movie).trim().toLowerCase().replace(/_/g, '-')
}

function movieState(movie: Movie) {
  if (movie.status) return movie.status
  if (availableFiles(movie).length > 0) return 'available'
  return movie.monitored ? 'missing' : 'unmonitored'
}

function movieFiles(movie: Movie): MovieFile[] {
  return movie.files ?? []
}

function availableFiles(movie: Movie): MovieFile[] {
  return movieFiles(movie).filter((file) => !file.missing)
}

function bestFile(movie: Movie): MovieFile | null {
  const files = availableFiles(movie)
  if (files.length === 0) return null
  return files.reduce((best, file) => (file.score >= best.score ? file : best))
}

function tagsOf(movie: Movie) {
  return strings(movie.tags)
}

// Local midnight of the release date; date-only values are read as written, not as UTC.
function releasedTimestamp(movie: Movie) {
  const value = (movie.metadata.released ?? '').trim()
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})/)
  const date = match
    ? new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
    : new Date(value)
  if (Number.isNaN(date.getTime())) return null
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime()
}

function parseDateBound(value: string) {
  if (!value) return null
  const parts = value.split('-').map(Number)
  if (parts.length !== 3 || parts.some((part) => !Number.isFinite(part))) return null
  const date = new Date(parts[0], parts[1] - 1, parts[2])
  return Number.isNaN(date.getTime()) ? null : date.getTime()
}

function parseRatingBound(value: string) {
  if (value.trim() === '') return null
  const rating = Number(value)
  return Number.isFinite(rating) ? rating : null
}

function formatDate(value: string | number | null) {
  if (value === null) return 'Unknown'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Unknown'
  return date.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

function formatDateTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? 'Unknown time' : date.toLocaleString()
}

// Unknown stays unknown: only a real positive rating counts.
function ratingValue(movie: Movie) {
  const rating = movie.metadata.rating
  return typeof rating === 'number' && rating > 0 ? rating : null
}

function ratingText(movie: Movie) {
  const rating = ratingValue(movie)
  return rating === null ? null : rating.toFixed(1)
}

function runtimeText(movie: Movie) {
  const runtime = movie.metadata.runtime
  if (!runtime || runtime <= 0) return null
  const hours = Math.floor(runtime / 60)
  const minutes = runtime % 60
  return hours > 0 ? `${hours}h ${minutes}m` : `${minutes}m`
}

type SortField =
  | 'title'
  | 'date'
  | 'released'
  | 'rating'
  | 'votes'
  | 'year'
  | 'runtime'
  | 'directors'
  | 'cast'
  | 'genres'
  | 'languages'
  | 'certification'
  | 'countries'
  | 'tags'
  | 'monitored'
  | 'profile'
  | 'quality'
  | 'status'

const sortFields: { value: SortField; label: string }[] = [
  { value: 'title', label: 'Title' },
  { value: 'date', label: 'Date added' },
  { value: 'released', label: 'Release date' },
  { value: 'rating', label: 'IMDb rating' },
  { value: 'votes', label: 'IMDb votes' },
  { value: 'year', label: 'Year' },
  { value: 'runtime', label: 'Runtime' },
  { value: 'directors', label: 'Director' },
  { value: 'cast', label: 'Cast' },
  { value: 'genres', label: 'Genre' },
  { value: 'languages', label: 'Language' },
  { value: 'certification', label: 'Certification' },
  { value: 'countries', label: 'Country' },
  { value: 'tags', label: 'Tags' },
  { value: 'monitored', label: 'Monitoring' },
  { value: 'profile', label: 'Profile' },
  { value: 'quality', label: 'Quality' },
  { value: 'status', label: 'Status' },
]

function sortValue(movie: Movie, field: SortField): string | number | null {
  const metadata = movie.metadata
  switch (field) {
    case 'title':
      return metadata.title || null
    case 'date': {
      const value = Date.parse(movie.addedAt)
      return Number.isFinite(value) ? value : null
    }
    case 'released':
      return releasedTimestamp(movie)
    case 'rating':
      return ratingValue(movie)
    case 'votes':
      return metadata.votes > 0 ? metadata.votes : null
    case 'year':
      return metadata.year > 0 ? metadata.year : null
    case 'runtime':
      return metadata.runtime > 0 ? metadata.runtime : null
    case 'directors':
      return first(metadata.directors)
    case 'cast':
      return first(metadata.cast)
    case 'genres':
      return first(metadata.genres)
    case 'languages':
      return first(metadata.languages)
    case 'certification':
      return metadata.certification || null
    case 'countries':
      return first(metadata.countries)
    case 'tags':
      return tagsOf(movie).join(', ') || null
    case 'monitored':
      return movie.monitored ? 1 : 0
    case 'profile':
      return movie.profileId || null
    case 'quality':
      return bestFile(movie)?.quality || null
    case 'status':
      return movieState(movie) || null
  }
}

// Unknown values stay last in both directions.
function compareMovies(a: Movie, b: Movie, field: SortField, ascending: boolean) {
  const left = sortValue(a, field)
  const right = sortValue(b, field)
  if (left === null && right === null) return 0
  if (left === null) return 1
  if (right === null) return -1
  const result =
    typeof left === 'number' && typeof right === 'number'
      ? left - right
      : String(left).localeCompare(String(right), undefined, { sensitivity: 'base', numeric: true })
  return ascending ? result : -result
}

function matchesSearch(movie: Movie, term: string) {
  const metadata = movie.metadata
  return [
    metadata.title,
    metadata.imdbId,
    metadata.year > 0 ? String(metadata.year) : '',
    metadata.certification,
    metadata.plot,
    movie.collection,
    ...strings(metadata.genres),
    ...strings(metadata.directors),
    ...strings(metadata.cast),
    ...strings(metadata.languages),
    ...strings(metadata.countries),
    ...tagsOf(movie),
  ]
    .join(' ')
    .toLowerCase()
    .includes(term)
}

type MetadataFilterKey =
  | 'genre'
  | 'director'
  | 'cast'
  | 'language'
  | 'country'
  | 'certification'
  | 'tag'
  | 'collection'

type MetadataFilter = {
  key: MetadataFilterKey
  label: string
  values: (movie: Movie) => string[]
}

const metadataFilters: MetadataFilter[] = [
  { key: 'genre', label: 'Genre', values: (movie) => strings(movie.metadata.genres) },
  { key: 'director', label: 'Director', values: (movie) => strings(movie.metadata.directors) },
  { key: 'cast', label: 'Cast', values: (movie) => strings(movie.metadata.cast) },
  { key: 'language', label: 'Language', values: (movie) => strings(movie.metadata.languages) },
  { key: 'country', label: 'Country', values: (movie) => strings(movie.metadata.countries) },
  {
    key: 'certification',
    label: 'Certification',
    values: (movie) => (movie.metadata.certification ? [movie.metadata.certification] : []),
  },
  { key: 'tag', label: 'Tag', values: tagsOf },
  { key: 'collection', label: 'Collection', values: (movie) => (movie.collection ? [movie.collection] : []) },
]

function emptyMetadataFilters(): Record<MetadataFilterKey, string> {
  return {
    genre: 'all',
    director: 'all',
    cast: 'all',
    language: 'all',
    country: 'all',
    certification: 'all',
    tag: 'all',
    collection: 'all',
  }
}

function Select({ className, children, ...props }: ComponentProps<'select'>) {
  return (
    <select
      className={cn(
        'h-9 rounded-md border border-input bg-transparent px-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 dark:bg-input/30',
        className,
      )}
      {...props}
    >
      {children}
    </select>
  )
}

function Checkbox({
  id,
  label,
  description,
  checked,
  onChange,
}: {
  id: string
  label: string
  description?: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-start gap-2.5">
      <input
        id={id}
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
        className="mt-0.5 size-4 shrink-0 accent-primary"
      />
      <div className="space-y-0.5">
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
    </div>
  )
}

function Poster({ title, poster, className }: { title: string; poster: string; className?: string }) {
  const [failed, setFailed] = useState(false)
  if (!poster || failed) {
    return (
      <div className={cn('flex items-center justify-center bg-muted', className)}>
        <FilmIcon className="size-7 text-muted-foreground/60" aria-hidden="true" />
      </div>
    )
  }
  return (
    <img
      src={poster}
      alt=""
      title={title || undefined}
      loading="lazy"
      onError={() => setFailed(true)}
      className={cn('object-cover', className)}
    />
  )
}

function StatusBadge({ movie }: { movie: Movie }) {
  const key = stateKey(movie)
  const label = statusLabels[key] ?? (movie.status ? movie.status.replace(/[-_]/g, ' ') : 'Unknown')
  return (
    <Badge
      variant={key === 'failed' ? 'destructive' : 'outline'}
      className={cn(statusTones[key], key === 'failed' && 'border-destructive/30 bg-destructive/10')}
    >
      {label}
    </Badge>
  )
}

function DialogShell({
  title,
  description,
  children,
  onClose,
  size = 'lg',
}: {
  title: string
  description: string
  children: ReactNode
  onClose: () => void
  size?: 'lg' | 'xl'
}) {
  const routeActive = useMoviesRoute()
  return (
    <Dialog.Root open={routeActive} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 motion-reduce:animate-none" />
        <Dialog.Content
          className={cn(
            'fixed top-1/2 left-1/2 z-50 flex max-h-[90vh] w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-border bg-card shadow-xl data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 motion-reduce:animate-none',
            size === 'xl' ? 'max-w-5xl' : 'max-w-2xl',
          )}
        >
          <header className="flex items-start justify-between gap-4 border-b border-border p-4">
            <div className="min-w-0">
              <Dialog.Title className="font-heading text-base font-semibold">{title}</Dialog.Title>
              <Dialog.Description className="mt-0.5 text-xs text-muted-foreground">
                {description}
              </Dialog.Description>
            </div>
            <Dialog.Close asChild>
              <Button variant="ghost" size="icon-sm" aria-label="Close dialog">
                <XIcon />
              </Button>
            </Dialog.Close>
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto p-4">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function MetaRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

function EmptyState({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-border px-4 py-6 text-sm text-muted-foreground">
      {children}
    </div>
  )
}

function ErrorNote({ children, onRetry }: { children: ReactNode; onRetry?: () => void }) {
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
    >
      <span className="min-w-0">{children}</span>
      {onRetry && (
        <Button size="sm" variant="outline" onClick={onRetry}>
          <RefreshCwIcon data-icon="inline-start" />
          Retry
        </Button>
      )}
    </div>
  )
}

function profileName(profiles: MovieProfile[], id: string) {
  return profiles.find((profile) => profile.id === id)?.name || id || 'Default'
}
function LibraryView({
  movies,
  profiles,
  roots,
  canWrite,
  canReadSettings,
  onOpenMovie,
  onAdd,
  onScan,
  onBulkEdit,
}: {
  movies: Movie[]
  profiles: MovieProfile[]
  roots: RootFolder[]
  canWrite: boolean
  canReadSettings: boolean
  onOpenMovie: (id: string) => void
  onAdd: () => void
  onScan: () => void
  onBulkEdit: (input: BulkEditInput) => Promise<void>
}) {
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('all')
  const [monitor, setMonitor] = useState('all')
  const [quality, setQuality] = useState('all')
  const [profile, setProfile] = useState('all')
  const [sort, setSort] = useState<SortField>('date')
  const [ascending, setAscending] = useState(false)
  const [view, setView] = useState<'grid' | 'table'>('grid')
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState({
    monitored: 'keep',
    profileId: 'keep',
    rootId: 'keep',
    tags: '',
    clearTags: false,
    collection: '',
    clearCollection: false,
  })
  const [bulkBusy, setBulkBusy] = useState(false)
  const [bulkError, setBulkError] = useState('')
  const [bulkNotice, setBulkNotice] = useState('')
  const [metaOpen, setMetaOpen] = useState(false)
  const [metaFilters, setMetaFilters] = useState<Record<MetadataFilterKey, string>>(emptyMetadataFilters)
  const [releasedFrom, setReleasedFrom] = useState('')
  const [releasedTo, setReleasedTo] = useState('')
  const [ratingMin, setRatingMin] = useState('')
  const [ratingMax, setRatingMax] = useState('')

  const qualityOptions = useMemo(() => {
    const values = new Set<string>()
    for (const movie of movies) {
      const file = bestFile(movie)
      if (file?.quality) values.add(file.quality)
    }
    return [...values].sort((a, b) => a.localeCompare(b))
  }, [movies])

  const statusOptions = useMemo(() => {
    const values = new Set<string>()
    for (const movie of movies) values.add(stateKey(movie))
    return [...values].sort()
  }, [movies])

  const metadataOptions = useMemo(() => {
    const options = new Map<MetadataFilterKey, string[]>()
    for (const filter of metadataFilters) {
      const values = new Set<string>()
      for (const movie of movies) for (const value of filter.values(movie)) values.add(value)
      options.set(filter.key, [...values].sort((a, b) => a.localeCompare(b)))
    }
    return options
  }, [movies])

  const activeMetadataCount =
    metadataFilters.filter((filter) => metaFilters[filter.key] !== 'all').length +
    (releasedFrom ? 1 : 0) +
    (releasedTo ? 1 : 0) +
    (ratingMin.trim() ? 1 : 0) +
    (ratingMax.trim() ? 1 : 0)

  const clearMetadataFilters = () => {
    setMetaFilters(emptyMetadataFilters())
    setReleasedFrom('')
    setReleasedTo('')
    setRatingMin('')
    setRatingMax('')
  }

  const visible = useMemo(() => {
    const term = query.trim().toLowerCase()
    const from = parseDateBound(releasedFrom)
    const to = parseDateBound(releasedTo)
    const minRating = parseRatingBound(ratingMin)
    const maxRating = parseRatingBound(ratingMax)
    return movies
      .filter((movie) => {
        const released = releasedTimestamp(movie)
        const rating = ratingValue(movie)
        return (
          (status === 'all' || stateKey(movie) === status) &&
          (monitor === 'all' || (monitor === 'monitored') === movie.monitored) &&
          (quality === 'all' || bestFile(movie)?.quality === quality) &&
          (profile === 'all' || movie.profileId === profile) &&
          (from === null || (released !== null && released >= from)) &&
          (to === null || (released !== null && released <= to)) &&
          (minRating === null || (rating !== null && rating >= minRating)) &&
          (maxRating === null || (rating !== null && rating <= maxRating)) &&
          metadataFilters.every((filter) => {
            const selected = metaFilters[filter.key]
            return selected === 'all' || filter.values(movie).includes(selected)
          }) &&
          (!term || matchesSearch(movie, term))
        )
      })
      .sort((a, b) => compareMovies(a, b, sort, ascending))
  }, [
    movies,
    query,
    status,
    monitor,
    quality,
    profile,
    metaFilters,
    releasedFrom,
    releasedTo,
    ratingMin,
    ratingMax,
    sort,
    ascending,
  ])

  const selected = useMemo(() => {
    const ids = new Set(movies.map((movie) => movie.id))
    return new Set([...selectedIds].filter((id) => ids.has(id)))
  }, [selectedIds, movies])

  const toggle = (id: string) =>
    setSelectedIds((previous) => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const toggleAll = () =>
    setSelectedIds((previous) =>
      previous.size === visible.length ? new Set() : new Set(visible.map((movie) => movie.id)),
    )

  const applyBulk = async () => {
    if (selected.size === 0) return
    const input: BulkEditInput = { ids: [...selected] }
    if (bulk.monitored !== 'keep') input.monitored = bulk.monitored === 'monitored'
    if (bulk.profileId !== 'keep') input.profileId = bulk.profileId
    if (bulk.rootId !== 'keep') input.rootId = bulk.rootId
    if (bulk.clearTags) input.tags = []
    else if (bulk.tags.trim()) input.tags = splitList(bulk.tags)
    if (bulk.clearCollection) input.collection = ''
    else if (bulk.collection.trim()) input.collection = bulk.collection.trim()
    if (Object.keys(input).length === 1) {
      setBulkError('Choose at least one change to apply.')
      return
    }
    setBulkBusy(true)
    setBulkError('')
    setBulkNotice('')
    try {
      await onBulkEdit(input)
      setBulkNotice(`Updated ${selected.size} movie(s).`)
      setSelectedIds(new Set())
      setBulk({
        monitored: 'keep',
        profileId: 'keep',
        rootId: 'keep',
        tags: '',
        clearTags: false,
        collection: '',
        clearCollection: false,
      })
    } catch (cause) {
      setBulkError(errorMessage(cause))
    } finally {
      setBulkBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-52 flex-1 sm:max-w-xs">
          <SearchIcon
            className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search title, cast, genre, tag…"
            aria-label="Search movies"
            className="pl-8"
          />
        </div>
        <Select aria-label="Filter by status" value={status} onChange={(event) => setStatus(event.target.value)}>
          <option value="all">All statuses</option>
          {statusOptions.map((value) => (
            <option key={value} value={value}>
              {statusLabels[value] ?? value}
            </option>
          ))}
        </Select>
        <Select aria-label="Filter by monitoring" value={monitor} onChange={(event) => setMonitor(event.target.value)}>
          <option value="all">All monitoring</option>
          <option value="monitored">Monitored</option>
          <option value="unmonitored">Unmonitored</option>
        </Select>
        {qualityOptions.length > 0 && (
          <Select aria-label="Filter by quality" value={quality} onChange={(event) => setQuality(event.target.value)}>
            <option value="all">All qualities</option>
            {qualityOptions.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        )}
        {profiles.length > 0 && (
          <Select aria-label="Filter by profile" value={profile} onChange={(event) => setProfile(event.target.value)}>
            <option value="all">All profiles</option>
            {profiles.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </Select>
        )}
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <div className="flex items-center rounded-md border border-border p-0.5">
            <Button
              variant={view === 'grid' ? 'secondary' : 'ghost'}
              size="icon-xs"
              aria-label="Poster grid"
              aria-pressed={view === 'grid'}
              onClick={() => setView('grid')}
            >
              <LayoutGridIcon />
            </Button>
            <Button
              variant={view === 'table' ? 'secondary' : 'ghost'}
              size="icon-xs"
              aria-label="Table view"
              aria-pressed={view === 'table'}
              onClick={() => setView('table')}
            >
              <ListIcon />
            </Button>
          </div>
          {canWrite && canReadSettings && (
            <Button size="sm" variant="outline" onClick={onScan}>
              <FolderSearchIcon data-icon="inline-start" />
              Scan library
            </Button>
          )}
          {canWrite && (
            <Button size="sm" onClick={onAdd}>
              <PlusIcon data-icon="inline-start" />
              Add movie
            </Button>
          )}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted-foreground" role="status">
          {visible.length === movies.length
            ? `${movies.length} ${movies.length === 1 ? 'movie' : 'movies'}`
            : `${visible.length} of ${movies.length} movies`}
        </span>
        <Button
          variant={metaOpen ? 'secondary' : 'outline'}
          size="sm"
          aria-expanded={metaOpen}
          aria-controls="metadata-filters"
          onClick={() => setMetaOpen((current) => !current)}
        >
          <ListFilterIcon data-icon="inline-start" />
          Metadata filters
          {activeMetadataCount > 0 && (
            <span className="rounded-full bg-muted px-1.5 text-[11px] font-semibold tabular-nums">
              {activeMetadataCount}
            </span>
          )}
        </Button>
        <div className="ml-auto flex items-center gap-2">
          <label htmlFor="movie-sort" className="text-muted-foreground">
            Sort
          </label>
          <Select id="movie-sort" value={sort} onChange={(event) => setSort(event.target.value as SortField)}>
            {sortFields.map((field) => (
              <option key={field.value} value={field.value}>
                {field.label}
              </option>
            ))}
          </Select>
          <Button
            variant="outline"
            size="icon-sm"
            aria-label={ascending ? 'Sort descending' : 'Sort ascending'}
            aria-pressed={ascending}
            onClick={() => setAscending((current) => !current)}
          >
            {ascending ? <ArrowUpIcon /> : <ArrowDownIcon />}
          </Button>
        </div>
      </div>

      <div
        id="metadata-filters"
        hidden={!metaOpen}
        className="grid gap-3 rounded-lg border border-border p-3 sm:grid-cols-2 lg:grid-cols-4"
      >
        {metadataFilters.map((filter) => (
          <div key={filter.key} className="space-y-1.5">
            <label htmlFor={`filter-${filter.key}`} className="text-xs font-medium text-muted-foreground">
              {filter.label}
            </label>
            <Select
              id={`filter-${filter.key}`}
              className="w-full"
              value={metaFilters[filter.key]}
              onChange={(event) => setMetaFilters({ ...metaFilters, [filter.key]: event.target.value })}
            >
              <option value="all">Any</option>
              {(metadataOptions.get(filter.key) ?? []).map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </Select>
          </div>
        ))}
        <div className="space-y-1.5">
          <label htmlFor="filter-released-from" className="text-xs font-medium text-muted-foreground">
            Released from
          </label>
          <Input
            id="filter-released-from"
            type="date"
            value={releasedFrom}
            onChange={(event) => setReleasedFrom(event.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <label htmlFor="filter-released-to" className="text-xs font-medium text-muted-foreground">
            Released to
          </label>
          <Input
            id="filter-released-to"
            type="date"
            value={releasedTo}
            onChange={(event) => setReleasedTo(event.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <label htmlFor="filter-rating-min" className="text-xs font-medium text-muted-foreground">
            IMDb rating min
          </label>
          <Input
            id="filter-rating-min"
            type="number"
            min="0"
            max="10"
            step="0.1"
            inputMode="decimal"
            placeholder="Any"
            value={ratingMin}
            onChange={(event) => setRatingMin(event.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <label htmlFor="filter-rating-max" className="text-xs font-medium text-muted-foreground">
            IMDb rating max
          </label>
          <Input
            id="filter-rating-max"
            type="number"
            min="0"
            max="10"
            step="0.1"
            inputMode="decimal"
            placeholder="Any"
            value={ratingMax}
            onChange={(event) => setRatingMax(event.target.value)}
          />
        </div>
        <p className="text-xs text-muted-foreground sm:col-span-2 lg:col-span-3">
          Unknown release dates and ratings stay visible unless a range is set.
        </p>
        <div className="flex items-end justify-end">
          <Button variant="outline" size="sm" disabled={activeMetadataCount === 0} onClick={clearMetadataFilters}>
            <XIcon data-icon="inline-start" />
            Clear filters
          </Button>
        </div>
      </div>

      {visible.length === 0 ? (
        <EmptyState>
          {movies.length === 0
            ? canWrite
              ? 'Your catalog is empty. Add a movie or scan an existing library folder.'
              : 'Your catalog is empty. An administrator or operator can add movies and scan folders.'
            : 'No movies match the current search and filters.'}
        </EmptyState>
      ) : view === 'grid' ? (
        <ul className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
          {visible.map((movie) => {
            const file = bestFile(movie)
            const rating = ratingText(movie)
            return (
              <li key={movie.id}>
                <Card size="sm" className="relative gap-0 overflow-hidden p-0 [--card-spacing:0px]">
                  <button
                    type="button"
                    onClick={() => onOpenMovie(movie.id)}
                    className="block w-full text-left focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                    aria-label={`Open ${movie.metadata.title || 'movie'} details`}
                  >
                    <Poster
                      title={movie.metadata.title}
                      poster={movie.metadata.poster}
                      className="aspect-2/3 w-full"
                    />
                    <div className="space-y-1.5 p-3">
                      <p className="truncate text-sm font-medium" title={movie.metadata.title}>
                        {movie.metadata.title || 'Untitled'}
                      </p>
                      <p className="truncate text-xs text-muted-foreground">
                        {movie.metadata.year > 0 ? movie.metadata.year : 'Year unknown'}
                        {file
                          ? ` · ${formatBytes(file.size)}`
                          : movieFiles(movie).length > 0
                            ? ' · File missing'
                            : ' · No file'}
                      </p>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <StatusBadge movie={movie} />
                        {rating && (
                          <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                            <StarIcon className="size-3" aria-hidden="true" />
                            {rating}
                          </span>
                        )}
                      </div>
                      <p className="truncate text-xs text-muted-foreground">
                        {file?.quality || 'Quality unknown'}
                        {movie.monitored ? '' : ' · Unmonitored'}
                      </p>
                    </div>
                  </button>
                  {canWrite && (
                    <span className="absolute top-2 left-2 rounded-md bg-background/85 p-1">
                      <input
                        type="checkbox"
                        className="size-4 accent-primary"
                        checked={selected.has(movie.id)}
                        onChange={() => toggle(movie.id)}
                        aria-label={`Select ${movie.metadata.title || 'movie'}`}
                      />
                    </span>
                  )}
                </Card>
              </li>
            )
          })}
        </ul>
      ) : (
        <div className="overflow-x-auto rounded-xl ring-1 ring-foreground/10">
          <table className="w-full min-w-[64rem] border-collapse text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                {canWrite && (
                  <th scope="col" className="w-10 px-3 py-2">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={visible.length > 0 && selected.size === visible.length}
                      onChange={toggleAll}
                      aria-label="Select all visible movies"
                    />
                  </th>
                )}
                <th scope="col" className="px-3 py-2 font-medium">Title</th>
                <th scope="col" className="px-3 py-2 font-medium">Status</th>
                <th scope="col" className="px-3 py-2 font-medium">Quality</th>
                <th scope="col" className="px-3 py-2 font-medium">Rating</th>
                <th scope="col" className="px-3 py-2 font-medium">Runtime</th>
                <th scope="col" className="px-3 py-2 font-medium">Monitored</th>
                <th scope="col" className="px-3 py-2 font-medium">Profile</th>
                <th scope="col" className="px-3 py-2 font-medium">Tags</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {visible.map((movie) => {
                const file = bestFile(movie)
                const rating = ratingText(movie)
                const runtime = runtimeText(movie)
                return (
                  <tr key={movie.id} className="align-top">
                    {canWrite && (
                      <td className="px-3 py-2.5">
                        <input
                          type="checkbox"
                          className="size-4 accent-primary"
                          checked={selected.has(movie.id)}
                          onChange={() => toggle(movie.id)}
                          aria-label={`Select ${movie.metadata.title || 'movie'}`}
                        />
                      </td>
                    )}
                    <td className="px-3 py-2.5">
                      <button
                        type="button"
                        onClick={() => onOpenMovie(movie.id)}
                        className="flex items-start gap-2 rounded-sm text-left focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                      >
                        <Poster
                          title={movie.metadata.title}
                          poster={movie.metadata.poster}
                          className="h-12 w-8 shrink-0 rounded-sm"
                        />
                        <span className="min-w-0">
                          <span className="block font-medium">{movie.metadata.title || 'Untitled'}</span>
                          <span className="block text-xs text-muted-foreground">
                            {movie.metadata.year > 0 ? movie.metadata.year : 'Year unknown'}
                            {movie.metadata.imdbId ? ` · ${movie.metadata.imdbId}` : ''}
                          </span>
                        </span>
                      </button>
                    </td>
                    <td className="px-3 py-2.5">
                      <StatusBadge movie={movie} />
                    </td>
                    <td className="px-3 py-2.5 text-muted-foreground">
                      {file
                        ? `${file.quality || 'Unknown'} · ${formatBytes(file.size)}`
                        : movieFiles(movie).length > 0
                          ? 'File missing'
                          : 'No file'}
                    </td>
                    <td className="px-3 py-2.5 text-muted-foreground">
                      {rating ? (
                        <span>
                          {rating}
                          {movie.metadata.votes > 0 && (
                            <span className="block text-xs">{movie.metadata.votes.toLocaleString()} votes</span>
                          )}
                        </span>
                      ) : (
                        'Unknown'
                      )}
                    </td>
                    <td className="px-3 py-2.5 text-muted-foreground">{runtime ?? 'Unknown'}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{movie.monitored ? 'Yes' : 'No'}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">
                      {profileName(profiles, movie.profileId)}
                    </td>
                    <td className="px-3 py-2.5">
                      {tagsOf(movie).length > 0 ? (
                        <span className="flex flex-wrap gap-1">
                          {tagsOf(movie).map((tag) => (
                            <Badge key={tag} variant="outline">
                              {tag}
                            </Badge>
                          ))}
                        </span>
                      ) : (
                        <span className="text-muted-foreground">None</span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {canWrite && selected.size > 0 && (
        <div className="sticky bottom-3 z-10 flex flex-col gap-3 rounded-xl border border-border bg-card/95 p-3 shadow-lg backdrop-blur">
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-sm font-medium">{selected.size} selected</p>
            <Button variant="ghost" size="sm" onClick={() => setSelectedIds(new Set())}>
              Clear selection
            </Button>
            {bulkNotice && (
              <span role="status" className="text-xs text-emerald-400">
                {bulkNotice}
              </span>
            )}
          </div>
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
            <Select
              aria-label="Bulk monitoring"
              value={bulk.monitored}
              onChange={(event) => setBulk({ ...bulk, monitored: event.target.value })}
            >
              <option value="keep">Monitoring: keep</option>
              <option value="monitored">Monitoring: monitored</option>
              <option value="unmonitored">Monitoring: unmonitored</option>
            </Select>
            <Select
              aria-label="Bulk quality profile"
              value={bulk.profileId}
              onChange={(event) => setBulk({ ...bulk, profileId: event.target.value })}
              disabled={profiles.length === 0}
            >
              <option value="keep">Profile: keep</option>
              {profiles.map((item) => (
                <option key={item.id} value={item.id}>
                  Profile: {item.name}
                </option>
              ))}
            </Select>
            <Select
              aria-label="Bulk root folder"
              value={bulk.rootId}
              onChange={(event) => setBulk({ ...bulk, rootId: event.target.value })}
              disabled={roots.length === 0}
            >
              <option value="keep">Root folder: keep</option>
              {roots.map((root) => (
                <option key={root.id || root.path} value={root.id}>
                  Root: {root.path}
                </option>
              ))}
            </Select>
            <Input
              value={bulk.tags}
              onChange={(event) => setBulk({ ...bulk, tags: event.target.value, clearTags: false })}
              placeholder="Tags (comma separated)"
              aria-label="Bulk tags"
            />
            <Input
              value={bulk.collection}
              onChange={(event) =>
                setBulk({ ...bulk, collection: event.target.value, clearCollection: false })
              }
              placeholder="Collection"
              aria-label="Bulk collection"
            />
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <input
                type="checkbox"
                className="size-4 accent-primary"
                checked={bulk.clearTags}
                onChange={(event) => setBulk({ ...bulk, clearTags: event.target.checked, tags: '' })}
              />
              Clear tags
            </label>
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <input
                type="checkbox"
                className="size-4 accent-primary"
                checked={bulk.clearCollection}
                onChange={(event) =>
                  setBulk({ ...bulk, clearCollection: event.target.checked, collection: '' })
                }
              />
              Clear collection
            </label>
            <Button size="sm" className="ml-auto" disabled={bulkBusy} onClick={() => void applyBulk()}>
              {bulkBusy ? (
                <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
              ) : (
                <PencilIcon data-icon="inline-start" />
              )}
              Apply to selected
            </Button>
          </div>
          {bulkError && (
            <p role="alert" className="text-sm text-destructive">
              {bulkError}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function WantedList({ movies, canWrite, onOpenMovie }: { movies: Movie[]; canWrite: boolean; onOpenMovie: (id: string) => void }) {
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<SyncResult | null>(null)

  const wanted = useMemo(
    () =>
      movies
        .filter(
          (movie) =>
            movie.monitored &&
            availableFiles(movie).length === 0 &&
            !activeStatuses.has(stateKey(movie)),
        )
        .sort((a, b) => (releasedTimestamp(b) ?? 0) - (releasedTimestamp(a) ?? 0)),
    [movies],
  )

  const runSync = async () => {
    setSyncing(true)
    setError('')
    setResult(null)
    try {
      setResult(await moviesApi.sync())
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setSyncing(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          {wanted.length} monitored {wanted.length === 1 ? 'movie has' : 'movies have'} no file yet.
          {!canWrite && ' Searching for releases requires library write access.'}
        </p>
        {canWrite && (
          <Button size="sm" disabled={syncing || wanted.length === 0} onClick={() => void runSync()}>
            {syncing ? (
              <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
            ) : (
              <SearchIcon data-icon="inline-start" />
            )}
            {syncing ? 'Searching…' : 'Search all wanted'}
          </Button>
        )}
      </div>

      {error && <ErrorNote onRetry={() => void runSync()}>{error}</ErrorNote>}
      {result && (
        <p role="status" className="rounded-md border border-emerald-500/20 bg-emerald-500/5 px-3 py-2 text-sm text-emerald-400">
          Searched {result.searched}, queued {result.queued}, imported {result.imported}.
        </p>
      )}

      {wanted.length === 0 ? (
        <EmptyState>
          Nothing is waiting for a file. Monitored movies without a file appear here.
        </EmptyState>
      ) : (
        <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
          {wanted.map((movie) => (
            <li key={movie.id} className="flex flex-wrap items-center gap-3 p-3">
              <Poster
                title={movie.metadata.title}
                poster={movie.metadata.poster}
                className="h-14 w-10 shrink-0 rounded-sm"
              />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium">{movie.metadata.title || 'Untitled'}</p>
                <p className="text-xs text-muted-foreground">
                  {movie.metadata.year > 0 ? movie.metadata.year : 'Year unknown'} · Released{' '}
                  {formatDate(releasedTimestamp(movie))}
                  {movie.lastSearchAt ? ` · Last search ${formatAge(movie.lastSearchAt)}` : ''}
                </p>
                {movie.error && <p className="text-xs text-destructive">{movie.error}</p>}
              </div>
              <StatusBadge movie={movie} />
              <Button size="sm" variant="outline" onClick={() => onOpenMovie(movie.id)}>
                <SearchIcon data-icon="inline-start" />
                {canWrite ? 'Search releases' : 'View details'}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
function CalendarTab({ onOpenMovie }: { onOpenMovie: (id: string) => void }) {
  const [movies, setMovies] = useState<Movie[] | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const result = await moviesApi.calendar(signal)
      if (!signal?.aborted) {
        setMovies(result)
        setError('')
      }
    } catch (cause) {
      if (!signal?.aborted) setError(errorMessage(cause))
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const groups = useMemo(() => {
    const map = new Map<string, { at: number | null; movies: Movie[] }>()
    for (const movie of movies ?? []) {
      const at = releasedTimestamp(movie)
      const key = at === null ? 'unknown' : new Date(at).toLocaleDateString(undefined, { year: 'numeric', month: 'long' })
      const group = map.get(key) ?? { at, movies: [] }
      group.movies.push(movie)
      map.set(key, group)
    }
    return [...map.entries()]
      .map(([key, group]) => ({
        key,
        at: group.at,
        movies: group.movies.sort((a, b) => (releasedTimestamp(a) ?? 0) - (releasedTimestamp(b) ?? 0)),
      }))
      .sort((a, b) => (a.at === null ? 1 : b.at === null ? -1 : a.at - b.at))
  }, [movies])

  if (error)
    return (
      <ErrorNote
        onRetry={() => {
          setMovies(null)
          setError('')
          void load()
        }}
      >
        {error}
      </ErrorNote>
    )
  if (movies === null) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
        Loading release calendar…
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          Release dates from movie metadata. Titles keep their catalog status.
        </p>
        <Button asChild size="sm" variant="outline">
          <a href={moviesApi.calendarIcsUrl()} download="constellarr-movies.ics">
            <CalendarIcon data-icon="inline-start" />
            Export .ics
          </a>
        </Button>
      </div>

      {groups.length === 0 ? (
        <EmptyState>No movies to place on the calendar yet.</EmptyState>
      ) : (
        groups.map((group) => (
          <section key={group.key} className="space-y-2">
            <h3 className="font-heading text-sm font-semibold">
              {group.key === 'unknown' ? 'No release date' : group.key}
            </h3>
            <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
              {group.movies.map((movie) => (
                <li key={movie.id} className="flex flex-wrap items-center gap-3 p-3">
                  <span className="w-24 shrink-0 text-sm text-muted-foreground">
                    {formatDate(releasedTimestamp(movie))}
                  </span>
                  <Poster
                    title={movie.metadata.title}
                    poster={movie.metadata.poster}
                    className="h-12 w-8 shrink-0 rounded-sm"
                  />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{movie.metadata.title || 'Untitled'}</p>
                    <p className="text-xs text-muted-foreground">
                      {movie.metadata.year > 0 ? movie.metadata.year : 'Year unknown'}
                      {movie.monitored ? '' : ' · Unmonitored'}
                    </p>
                  </div>
                  <StatusBadge movie={movie} />
                  <Button size="sm" variant="outline" onClick={() => onOpenMovie(movie.id)}>
                    Open
                  </Button>
                </li>
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  )
}

function ActivityTab({ movies, onOpenMovie }: { movies: Movie[]; onOpenMovie: (id: string) => void }) {
  const [entries, setEntries] = useState<HistoryEntry[] | null>(null)
  const [error, setError] = useState('')
  const [type, setType] = useState('all')

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const result = await moviesApi.allHistory(signal)
      if (!signal?.aborted) {
        setEntries(result)
        setError('')
      }
    } catch (cause) {
      if (!signal?.aborted) setError(errorMessage(cause))
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const titles = useMemo(() => new Map(movies.map((movie) => [movie.id, movie.metadata.title])), [movies])
  const types = useMemo(
    () => [...new Set((entries ?? []).map((entry) => entry.type).filter(Boolean))].sort(),
    [entries],
  )
  const visible = useMemo(
    () => (entries ?? []).filter((entry) => type === 'all' || entry.type === type),
    [entries, type],
  )

  if (error)
    return (
      <ErrorNote
        onRetry={() => {
          setEntries(null)
          setError('')
          void load()
        }}
      >
        {error}
      </ErrorNote>
    )
  if (entries === null) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
        Loading history…
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <p className="text-sm text-muted-foreground">
          {visible.length} of {entries.length} events
        </p>
        {types.length > 0 && (
          <Select aria-label="Filter history by type" value={type} onChange={(event) => setType(event.target.value)} className="ml-auto">
            <option value="all">All event types</option>
            {types.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        )}
      </div>

      {visible.length === 0 ? (
        <EmptyState>No history yet. Acquisitions, imports, and refreshes appear here.</EmptyState>
      ) : (
        <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
          {visible.map((entry) => (
            <li key={entry.id} className="flex flex-wrap items-start gap-3 p-3">
              <Badge variant="outline" className="capitalize">
                {entry.type || 'event'}
              </Badge>
              <div className="min-w-0 flex-1">
                <p className="text-sm break-words">{entry.message}</p>
                <p className="text-xs text-muted-foreground">
                  {titles.get(entry.movieId) ?? entry.movieId} ·{' '}
                  <time dateTime={entry.createdAt} title={formatDateTime(entry.createdAt)}>
                    {formatAge(entry.createdAt)}
                  </time>
                </p>
              </div>
              {movies.some((movie) => movie.id === entry.movieId) && (
                <Button size="sm" variant="ghost" onClick={() => onOpenMovie(entry.movieId)}>
                  Open
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

type ProfileDraft = {
  id: string
  name: string
  qualities: string
  cutoff: string
  upgrade: boolean
  minMB: string
  maxMB: string
  language: string
  minScore: string
  cutoffScore: string
  rules: QualityRule[]
}

function profileDraft(profile: MovieProfile): ProfileDraft {
  return {
    id: profile.id,
    name: profile.name,
    qualities: strings(profile.qualities).join(', '),
    cutoff: profile.cutoff,
    upgrade: profile.upgrade,
    minMB: String(profile.minMB || 0),
    maxMB: String(profile.maxMB || 0),
    language: profile.language,
    minScore: String(profile.minScore || 0),
    cutoffScore: String(profile.cutoffScore || 0),
    rules: (profile.rules ?? []).map((rule) => ({ ...rule })),
  }
}

function emptyProfileDraft(): ProfileDraft {
  return {
    id: '',
    name: '',
    qualities: '',
    cutoff: '',
    upgrade: true,
    minMB: '0',
    maxMB: '0',
    language: '',
    minScore: '0',
    cutoffScore: '0',
    rules: [],
  }
}

function draftToProfile(draft: ProfileDraft): MovieProfile {
  return {
    id: draft.id,
    name: draft.name.trim(),
    qualities: splitList(draft.qualities),
    cutoff: draft.cutoff.trim(),
    upgrade: draft.upgrade,
    minMB: Number(draft.minMB) || 0,
    maxMB: Number(draft.maxMB) || 0,
    language: draft.language.trim(),
    minScore: Number(draft.minScore) || 0,
    cutoffScore: Number(draft.cutoffScore) || 0,
    rules: draft.rules.map((rule) => ({
      name: rule.name.trim(),
      pattern: rule.pattern,
      score: Number(rule.score) || 0,
      required: rule.required,
      negate: rule.negate,
    })),
  }
}

export function QualityProfilesTab({ profiles, canWrite, onChanged }: { profiles: MovieProfile[]; canWrite: boolean; onChanged: () => void }) {
  const fieldId = useId()
  const [selectedId, setSelectedId] = useState<string | null>(profiles[0]?.id ?? null)
  const [draft, setDraft] = useState<ProfileDraft>(() =>
    profiles[0] ? profileDraft(profiles[0]) : emptyProfileDraft(),
  )
  const [busy, setBusy] = useState<'save' | 'delete' | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)

  const qualities = splitList(draft.qualities)
  const cutoffOptions = [...new Set([...qualities, draft.cutoff].filter(Boolean))]

  const choose = (profile: MovieProfile) => {
    setSelectedId(profile.id)
    setDraft(profileDraft(profile))
    setError('')
    setNotice('')
    setConfirmDelete(false)
  }

  const moveQuality = (index: number, offset: number) => {
    const next = [...qualities]
    const target = index + offset
    if (target < 0 || target >= next.length) return
    const [item] = next.splice(index, 1)
    next.splice(target, 0, item)
    setDraft({ ...draft, qualities: next.join(', ') })
  }

  const updateRule = (index: number, patch: Partial<QualityRule>) => {
    setDraft({
      ...draft,
      rules: draft.rules.map((rule, ruleIndex) => (ruleIndex === index ? { ...rule, ...patch } : rule)),
    })
  }

  const save = async () => {
    if (!draft.name.trim()) {
      setError('Give the profile a name.')
      return
    }
    setBusy('save')
    setError('')
    setNotice('')
    try {
      const saved = await moviesApi.saveProfile(draftToProfile(draft))
      setSelectedId(saved.id)
      setDraft(profileDraft(saved))
      setNotice(`Saved “${saved.name}”.`)
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const remove = async () => {
    if (!selectedId) return
    setBusy('delete')
    setError('')
    setNotice('')
    try {
      await moviesApi.deleteProfile(selectedId)
      setSelectedId(null)
      setDraft(emptyProfileDraft())
      setConfirmDelete(false)
      setNotice('Profile deleted.')
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,16rem)_minmax(0,1fr)]">
      <div className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-sm font-medium">Quality profiles</h3>
          {canWrite && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setSelectedId(null)
                setDraft(emptyProfileDraft())
                setError('')
                setNotice('')
                setConfirmDelete(false)
              }}
            >
              <PlusIcon data-icon="inline-start" />
              New
            </Button>
          )}
        </div>
        {profiles.length === 0 ? (
          <EmptyState>No profiles yet. Create one to control quality and upgrades.</EmptyState>
        ) : (
          <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
            {profiles.map((profile) => (
              <li key={profile.id}>
                <button
                  type="button"
                  onClick={() => choose(profile)}
                  aria-current={selectedId === profile.id ? 'true' : undefined}
                  className={cn(
                    'w-full p-3 text-left focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
                    selectedId === profile.id && 'bg-muted/60',
                  )}
                >
                  <p className="flex items-center gap-2 text-sm font-medium">
                    {profile.name || 'Untitled profile'}
                    {profile.upgrade && <Badge variant="outline">Upgrades</Badge>}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {strings(profile.qualities).length} qualities
                    {profile.cutoff ? ` · cutoff ${profile.cutoff}` : ''}
                  </p>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle>{draft.id ? `Edit ${draft.name || 'profile'}` : 'New profile'}</CardTitle>
          <CardDescription>
            Qualities are ordered best first. Rules score releases by pattern.
          </CardDescription>
        </CardHeader>
        <CardContent className="gap-5">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <label htmlFor={`${fieldId}-profile-name`} className="text-sm font-medium">
                Name
              </label>
              <Input
                id={`${fieldId}-profile-name`}
                value={draft.name}
                placeholder="HD Bluray"
                onChange={(event) => setDraft({ ...draft, name: event.target.value })}
              />
            </div>
            <div className="space-y-2">
              <label htmlFor={`${fieldId}-profile-cutoff`} className="text-sm font-medium">
                Upgrade cutoff
              </label>
              <Select
                id={`${fieldId}-profile-cutoff`}
                className="h-9 w-full"
                value={draft.cutoff}
                onChange={(event) => setDraft({ ...draft, cutoff: event.target.value })}
              >
                <option value="">Best allowed quality</option>
                {cutoffOptions.map((quality) => (
                  <option key={quality} value={quality}>
                    {quality}
                  </option>
                ))}
              </Select>
              <p className="text-xs text-muted-foreground">
                Without a cutoff, upgrades stop at your most preferred allowed quality.
              </p>
            </div>
          </div>

          <div className="space-y-2">
            <label htmlFor={`${fieldId}-profile-qualities`} className="text-sm font-medium">
              Allowed qualities, best first
            </label>
            <Input
              id={`${fieldId}-profile-qualities`}
              value={draft.qualities}
              placeholder="WEB-2160p, Bluray-1080p, WEB-1080p"
              onChange={(event) => setDraft({ ...draft, qualities: event.target.value })}
            />
            <p className="text-xs text-muted-foreground">Comma separated. Use the arrows to reorder.</p>
            {qualities.length > 0 && (
              <ul className="flex flex-wrap gap-1.5 pt-1">
                {qualities.map((quality, index) => (
                  <li
                    key={`${quality}-${index}`}
                    className="flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs"
                  >
                    <span>{quality}</span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label={`Move ${quality} earlier`}
                      disabled={index === 0}
                      onClick={() => moveQuality(index, -1)}
                    >
                      <ArrowUpIcon />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label={`Move ${quality} later`}
                      disabled={index === qualities.length - 1}
                      onClick={() => moveQuality(index, 1)}
                    >
                      <ArrowDownIcon />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      aria-label={`Remove ${quality}`}
                      onClick={() =>
                        setDraft({ ...draft, qualities: qualities.filter((_, i) => i !== index).join(', ') })
                      }
                    >
                      <XIcon />
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <div className="space-y-2">
              <label htmlFor={`${fieldId}-profile-min`} className="text-sm font-medium">
                Minimum size (MB)
              </label>
              <Input
                id={`${fieldId}-profile-min`}
                type="number"
                min="0"
                value={draft.minMB}
                onChange={(event) => setDraft({ ...draft, minMB: event.target.value })}
              />
            </div>
            <div className="space-y-2">
              <label htmlFor={`${fieldId}-profile-max`} className="text-sm font-medium">
                Maximum size (MB)
              </label>
              <Input
                id={`${fieldId}-profile-max`}
                type="number"
                min="0"
                value={draft.maxMB}
                onChange={(event) => setDraft({ ...draft, maxMB: event.target.value })}
              />
            </div>
            <div className="space-y-2">
              <label htmlFor={`${fieldId}-profile-language`} className="text-sm font-medium">
                Language
              </label>
              <Input
                id={`${fieldId}-profile-language`}
                value={draft.language}
                placeholder="English"
                onChange={(event) => setDraft({ ...draft, language: event.target.value })}
              />
            </div>
            <Checkbox
              id={`${fieldId}-profile-upgrade`}
              label="Allow upgrades"
              description="Replace a file when a better release passes the cutoff."
              checked={draft.upgrade}
              onChange={(upgrade) => setDraft({ ...draft, upgrade })}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <div className="space-y-2">
              <label htmlFor={`${fieldId}-profile-min-score`} className="text-sm font-medium">
                Minimum score
              </label>
              <Input
                id={`${fieldId}-profile-min-score`}
                type="number"
                value={draft.minScore}
                onChange={(event) => setDraft({ ...draft, minScore: event.target.value })}
              />
            </div>
            <div className="space-y-2">
              <label htmlFor={`${fieldId}-profile-cutoff-score`} className="text-sm font-medium">
                Cutoff score
              </label>
              <Input
                id={`${fieldId}-profile-cutoff-score`}
                type="number"
                value={draft.cutoffScore}
                onChange={(event) => setDraft({ ...draft, cutoffScore: event.target.value })}
              />
            </div>
          </div>

          <div className="space-y-3">
            <div className="flex items-center justify-between gap-2">
              <h4 className="text-sm font-medium">Release rules</h4>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() =>
                  setDraft({
                    ...draft,
                    rules: [...draft.rules, { name: '', pattern: '', score: 0, required: false, negate: false }],
                  })
                }
              >
                <PlusIcon data-icon="inline-start" />
                Add rule
              </Button>
            </div>
            {draft.rules.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                No rules. Add patterns such as “Remux” or “CAM” to score or reject releases.
              </p>
            ) : (
              <ul className="space-y-2">
                {draft.rules.map((rule, index) => (
                  <li
                    key={index}
                    className="grid items-center gap-2 rounded-lg border border-border p-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)_4.5rem_auto]"
                  >
                    <Input
                      value={rule.name}
                      aria-label={`Rule ${index + 1} name`}
                      placeholder="Rule name"
                      onChange={(event) => updateRule(index, { name: event.target.value })}
                    />
                    <Input
                      value={rule.pattern}
                      aria-label={`Rule ${index + 1} pattern`}
                      placeholder="Regex pattern"
                      className="font-mono text-xs"
                      onChange={(event) => updateRule(index, { pattern: event.target.value })}
                    />
                    <Input
                      type="number"
                      value={rule.score}
                      aria-label={`Rule ${index + 1} score`}
                      onChange={(event) => updateRule(index, { score: Number(event.target.value) || 0 })}
                    />
                    <div className="flex items-center gap-3">
                      <label className="flex items-center gap-1.5 text-xs">
                        <input
                          type="checkbox"
                          className="size-4 accent-primary"
                          checked={rule.required}
                          onChange={(event) => updateRule(index, { required: event.target.checked })}
                        />
                        Require match
                      </label>
                      <label className="flex items-center gap-1.5 text-xs">
                        <input
                          type="checkbox"
                          className="size-4 accent-primary"
                          checked={rule.negate}
                          onChange={(event) => updateRule(index, { negate: event.target.checked })}
                        />
                        Reject matches
                      </label>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Remove ${rule.name || `rule ${index + 1}`}`}
                        onClick={() =>
                          setDraft({ ...draft, rules: draft.rules.filter((_, i) => i !== index) })
                        }
                      >
                        <Trash2Icon />
                      </Button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </div>

          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          {notice && (
            <p role="status" className="text-sm text-emerald-400">
              <CheckIcon className="mr-2 inline size-4" />
              {notice}
            </p>
          )}

          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border pt-4">
            {canWrite &&
              (confirmDelete ? (
              <>
                <p className="mr-auto text-sm text-muted-foreground">
                  Delete this profile? Movies keep their files.
                </p>
                <Button type="button" variant="outline" size="sm" onClick={() => setConfirmDelete(false)}>
                  Cancel
                </Button>
                <Button
                  type="button"
                  variant="destructive"
                  size="sm"
                  disabled={busy === 'delete'}
                  onClick={() => void remove()}
                >
                  {busy === 'delete' ? (
                    <LoaderCircleIcon className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <Trash2Icon />
                  )}
                  Delete profile
                </Button>
              </>
            ) : (
              <>
                {selectedId && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="mr-auto"
                    onClick={() => setConfirmDelete(true)}
                  >
                    <Trash2Icon data-icon="inline-start" />
                    Delete
                  </Button>
                )}
                <Button type="button" size="sm" disabled={busy === 'save'} onClick={() => void save()}>
                  {busy === 'save' ? (
                    <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <SaveIcon data-icon="inline-start" />
                  )}
                  {draft.id ? 'Save profile' : 'Create profile'}
                </Button>
              </>
            ))}
          </div>
          {!canWrite && (
            <p className="border-t border-border pt-4 text-xs text-muted-foreground">
              Your role can view quality profiles but not change them.
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
type WatchlistDraft = Watchlist & { imdbIdsText: string }

function emptyWatchlist(profiles: MovieProfile[], roots: RootFolder[]): Watchlist {
  return {
    id: '',
    name: '',
    url: '',
    imdbIds: [],
    monitor: true,
    profileId: profiles[0]?.id ?? '',
    rootId: roots[0]?.id ?? '',
    intervalHours: 24,
    lastSyncAt: null,
    error: '',
  }
}

function WatchlistDialog({
  list,
  profiles,
  roots,
  onClose,
  onSaved,
}: {
  list: Watchlist
  profiles: MovieProfile[]
  roots: RootFolder[]
  onClose: () => void
  onSaved: (list: Watchlist) => void
}) {
  const [draft, setDraft] = useState<WatchlistDraft>(() => ({
    ...list,
    imdbIdsText: strings(list.imdbIds).join('\n'),
  }))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    if (!draft.name.trim()) {
      setError('Give the watchlist a name.')
      return
    }
    setBusy(true)
    setError('')
    try {
      const { imdbIdsText, ...rest } = draft
      const saved = await moviesApi.saveWatchlist({ ...rest, imdbIds: splitList(imdbIdsText) })
      onSaved(saved)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <DialogShell
      title={draft.id ? 'Edit watchlist' : 'New watchlist'}
      description="Import IMDb IDs from a list or by pasting them, then sync on a schedule."
      onClose={onClose}
    >
      <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <label htmlFor="watchlist-name" className="text-sm font-medium">
              Name
            </label>
            <Input
              id="watchlist-name"
              value={draft.name}
              placeholder="Awards shortlist"
              onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            />
          </div>
          <div className="space-y-2">
            <label htmlFor="watchlist-url" className="text-sm font-medium">
              List URL
            </label>
            <Input
              id="watchlist-url"
              type="url"
              value={draft.url}
              placeholder="https://www.imdb.com/list/ls000000000/"
              onChange={(event) => setDraft({ ...draft, url: event.target.value })}
            />
            <p className="text-xs text-muted-foreground">
              Optional. The server reads IMDb IDs from this list during sync.
            </p>
          </div>
        </div>

        <div className="space-y-2">
          <label htmlFor="watchlist-ids" className="text-sm font-medium">
            IMDb IDs
          </label>
          <textarea
            id="watchlist-ids"
            value={draft.imdbIdsText}
            rows={5}
            spellCheck={false}
            placeholder={'tt0111161\ntt0068646, tt0071562'}
            onChange={(event) => setDraft({ ...draft, imdbIdsText: event.target.value })}
            className="w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/30"
          />
          <p className="text-xs text-muted-foreground">
            Paste a CSV or one ID per line. Currently {splitList(draft.imdbIdsText).length} entries.
          </p>
        </div>

        <div className="grid gap-4 sm:grid-cols-3">
          <div className="space-y-2">
            <label htmlFor="watchlist-profile" className="text-sm font-medium">
              Quality profile
            </label>
            <Select
              id="watchlist-profile"
              className="h-9 w-full"
              value={draft.profileId}
              onChange={(event) => setDraft({ ...draft, profileId: event.target.value })}
            >
              <option value="">Default</option>
              {profiles.map((profile) => (
                <option key={profile.id} value={profile.id}>
                  {profile.name}
                </option>
              ))}
            </Select>
          </div>
          <div className="space-y-2">
            <label htmlFor="watchlist-root" className="text-sm font-medium">
              Root folder
            </label>
            <Select
              id="watchlist-root"
              className="h-9 w-full"
              value={draft.rootId}
              onChange={(event) => setDraft({ ...draft, rootId: event.target.value })}
            >
              <option value="">Default</option>
              {roots.map((root) => (
                <option key={root.id || root.path} value={root.id}>
                  {root.path}
                </option>
              ))}
            </Select>
          </div>
          <div className="space-y-2">
            <label htmlFor="watchlist-interval" className="text-sm font-medium">
              Sync interval (hours)
            </label>
            <Input
              id="watchlist-interval"
              type="number"
              min="1"
              value={draft.intervalHours}
              onChange={(event) =>
                setDraft({ ...draft, intervalHours: Number(event.target.value) || 1 })
              }
            />
          </div>
        </div>

        <Checkbox
          id="watchlist-monitor"
          label="Monitor added movies"
          description="New entries are searched automatically after import."
          checked={draft.monitor}
          onChange={(monitor) => setDraft({ ...draft, monitor })}
        />

        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <Button type="button" variant="outline" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button type="button" size="sm" disabled={busy} onClick={() => void save()}>
            {busy ? (
              <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
            ) : (
              <SaveIcon data-icon="inline-start" />
            )}
            Save watchlist
          </Button>
        </div>
      </div>
    </DialogShell>
  )
}

function WatchlistsTab({ profiles, roots, canWrite }: { profiles: MovieProfile[]; roots: RootFolder[]; canWrite: boolean }) {
  const [lists, setLists] = useState<Watchlist[] | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [editing, setEditing] = useState<Watchlist | null>(null)
  const [syncing, setSyncing] = useState('')
  const [removing, setRemoving] = useState('')
  const [confirming, setConfirming] = useState('')

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const result = await moviesApi.watchlists(signal)
      if (!signal?.aborted) {
        setLists(result)
        setError('')
      }
    } catch (cause) {
      if (!signal?.aborted) setError(errorMessage(cause))
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const sync = async (list: Watchlist) => {
    setSyncing(list.id)
    setError('')
    setNotice('')
    try {
      const result = await moviesApi.syncWatchlist(list.id)
      setNotice(`${list.name || 'Watchlist'}: added ${result.added} movie(s).`)
      notifyMoviesChanged()
      await load()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setSyncing('')
    }
  }

  const remove = async (list: Watchlist) => {
    setRemoving(list.id)
    setError('')
    setNotice('')
    try {
      await moviesApi.deleteWatchlist(list.id)
      setConfirming('')
      setNotice('Watchlist deleted. Movies stay in the catalog.')
      await load()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setRemoving('')
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          Sync IMDb lists on a schedule. New titles are added to the catalog and watched for releases.
          {!canWrite && ' Changing watchlists requires settings write access.'}
        </p>
        {canWrite && (
          <Button size="sm" onClick={() => setEditing(emptyWatchlist(profiles, roots))}>
            <PlusIcon data-icon="inline-start" />
            New watchlist
          </Button>
        )}
      </div>

      {error && <ErrorNote onRetry={() => void load()}>{error}</ErrorNote>}
      {notice && (
        <p role="status" className="rounded-md border border-emerald-500/20 bg-emerald-500/5 px-3 py-2 text-sm text-emerald-400">
          <CheckIcon className="mr-2 inline size-4" />
          {notice}
        </p>
      )}

      {lists === null ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
          Loading watchlists…
        </p>
      ) : lists.length === 0 ? (
        <EmptyState>No watchlists yet. Add one to import IMDb lists automatically.</EmptyState>
      ) : (
        <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
          {lists.map((list) => (
            <li key={list.id} className="flex flex-col gap-2 p-3">
              <div className="flex flex-wrap items-start gap-3">
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
                    {list.name || 'Untitled watchlist'}
                    <Badge variant="outline">{list.monitor ? 'Monitored' : 'Not monitored'}</Badge>
                  </p>
                  {list.url && (
                    <a
                      href={list.url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-xs break-all text-primary underline-offset-4 hover:underline"
                    >
                      {list.url}
                    </a>
                  )}
                  <p className="text-xs text-muted-foreground">
                    {strings(list.imdbIds).length} IMDb IDs · every {list.intervalHours}h
                    {list.lastSyncAt ? ` · last sync ${formatAge(list.lastSyncAt)}` : ' · never synced'}
                  </p>
                  {list.error && <p className="text-xs text-destructive">{list.error}</p>}
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  {canWrite && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={syncing === list.id}
                      onClick={() => void sync(list)}
                    >
                      {syncing === list.id ? (
                        <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                      ) : (
                        <RefreshCwIcon data-icon="inline-start" />
                      )}
                      {syncing === list.id ? 'Syncing…' : 'Sync now'}
                    </Button>
                  )}
                  {canWrite && (
                    <Button size="sm" variant="ghost" onClick={() => setEditing(list)}>
                      <PencilIcon data-icon="inline-start" />
                      Edit
                    </Button>
                  )}
                </div>
              </div>
              {confirming === list.id && (
                <div className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
                  <p className="mr-auto text-sm text-muted-foreground">
                    Delete this watchlist? Movies and files stay in the catalog.
                  </p>
                  <Button size="sm" variant="outline" onClick={() => setConfirming('')}>
                    Cancel
                  </Button>
                  <Button
                    size="sm"
                    variant="destructive"
                    disabled={removing === list.id}
                    onClick={() => void remove(list)}
                  >
                    {removing === list.id ? (
                      <LoaderCircleIcon className="animate-spin motion-reduce:animate-none" />
                    ) : (
                      <Trash2Icon />
                    )}
                    Delete
                  </Button>
                </div>
              )}
              {canWrite && (
                <div className="flex justify-end">
                  <Button size="sm" variant="ghost" onClick={() => setConfirming(list.id)}>
                    Delete watchlist
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      {canWrite && editing && (
        <WatchlistDialog
          list={editing}
          profiles={profiles}
          roots={roots}
          onClose={() => setEditing(null)}
          onSaved={(saved) => {
            setEditing(null)
            setNotice(`Saved “${saved.name || 'watchlist'}”.`)
            notifyMoviesChanged()
            void load()
          }}
        />
      )}
    </div>
  )
}
function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="space-y-3 border-t border-border pt-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-heading text-sm font-semibold">{title}</h3>
        {action}
      </div>
      {children}
    </section>
  )
}

function MovieDetailDialog({
  movie,
  profiles,
  roots,
  canWrite,
  canReadSettings,
  autoSearch,
  onClose,
  onSave,
  onRemove,
  onRefreshed,
  onGrabbed,
  onChanged,
}: {
  movie: Movie
  profiles: MovieProfile[]
  roots: RootFolder[]
  canWrite: boolean
  canReadSettings: boolean
  autoSearch: boolean
  onClose: () => void
  onSave: (movie: Movie) => Promise<Movie>
  onRemove: (id: string) => Promise<void>
  onRefreshed: (movie: Movie) => void
  onGrabbed: (id: string) => void
  onChanged: () => void
}) {
  const [form, setForm] = useState(() => ({
    monitored: movie.monitored,
    profileId: movie.profileId,
    rootId: movie.rootId,
    tags: tagsOf(movie).join(', '),
    collection: movie.collection,
  }))
  const [saving, setSaving] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [renaming, setRenaming] = useState<'preview' | 'apply' | null>(null)
  const [renameResult, setRenameResult] = useState<RenameResult | null>(null)
  const [removing, setRemoving] = useState(false)
  const [confirmRemove, setConfirmRemove] = useState(false)
  const [releases, setReleases] = useState<MovieRelease[] | null>(null)
  const [searching, setSearching] = useState(autoSearch)
  const [grabbing, setGrabbing] = useState('')
  const [history, setHistory] = useState<HistoryEntry[] | null>(null)
  const [historyError, setHistoryError] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const searchController = useRef<AbortController | null>(null)

  const tagsChanged = form.tags !== tagsOf(movie).join(', ')
  const dirty =
    form.monitored !== movie.monitored ||
    form.profileId !== movie.profileId ||
    form.rootId !== movie.rootId ||
    form.collection !== movie.collection ||
    tagsChanged

  useEffect(() => {
    const controller = new AbortController()
    moviesApi
      .history(movie.id, controller.signal)
      .then((entries) => {
        if (!controller.signal.aborted) setHistory(entries)
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setHistoryError(errorMessage(cause))
      })
    return () => controller.abort()
  }, [movie.id])

  const runReleaseSearch = useCallback(
    async (signal: AbortSignal) => {
      try {
        const found = await moviesApi.searchReleases(movie.id, signal)
        if (!signal.aborted) {
          setReleases(found)
          setError('')
        }
      } catch (cause) {
        if (!signal.aborted) setError(errorMessage(cause))
      } finally {
        if (!signal.aborted) setSearching(false)
      }
    },
    [movie.id],
  )

  const search = () => {
    searchController.current?.abort()
    const controller = new AbortController()
    searchController.current = controller
    setSearching(true)
    setError('')
    setNotice('')
    void runReleaseSearch(controller.signal)
  }

  useEffect(() => {
    if (!autoSearch || !canWrite) return
    const controller = new AbortController()
    searchController.current = controller
    // eslint-disable-next-line react-hooks/set-state-in-effect -- results are applied after the request settles
    void runReleaseSearch(controller.signal)
    return () => controller.abort()
  }, [autoSearch, canWrite, runReleaseSearch])

  useEffect(() => () => searchController.current?.abort(), [])

  const save = async () => {
    setSaving(true)
    setError('')
    setNotice('')
    try {
      const saved = await onSave({
        ...movie,
        monitored: form.monitored,
        profileId: form.profileId,
        rootId: form.rootId,
        tags: splitList(form.tags),
        collection: form.collection.trim(),
      })
      setForm({
        monitored: saved.monitored,
        profileId: saved.profileId,
        rootId: saved.rootId,
        tags: tagsOf(saved).join(', '),
        collection: saved.collection,
      })
      setNotice('Movie settings saved.')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setSaving(false)
    }
  }

  const refresh = async () => {
    setRefreshing(true)
    setError('')
    setNotice('')
    try {
      onRefreshed(await moviesApi.refresh(movie.id))
      setNotice('Metadata refreshed.')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setRefreshing(false)
    }
  }

  const runRename = async (preview: boolean) => {
    setRenaming(preview ? 'preview' : 'apply')
    setError('')
    setNotice('')
    try {
      const result = await moviesApi.rename(movie.id, preview)
      setRenameResult(result)
      if (!preview && result.applied) onChanged()
      setNotice(
        preview
          ? `${result.files.length} file(s) checked. Nothing changed yet.`
          : result.applied
            ? 'Files renamed.'
            : 'Rename applied with no changes.',
      )
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setRenaming(null)
    }
  }

  const remove = async () => {
    setRemoving(true)
    setError('')
    try {
      await onRemove(movie.id)
      onClose()
    } catch (cause) {
      setError(errorMessage(cause))
      setRemoving(false)
    }
  }

  const grab = async (release: MovieRelease) => {
    setGrabbing(release.id)
    setError('')
    setNotice('')
    try {
      const job = await moviesApi.grab(movie.id, release.id, !release.decision.allowed)
      onGrabbed(movie.id)
      setNotice(`Download queued as job ${job.id.slice(0, 8)}. Track it in Usenet.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setGrabbing('')
    }
  }

  const files = movieFiles(movie)
  const metadata = movie.metadata
  const rating = ratingText(movie)
  const runtime = runtimeText(movie)

  return (
    <DialogShell
      title={metadata.title || 'Movie details'}
      description={`${metadata.year > 0 ? metadata.year : 'Year unknown'} · ${statusLabels[stateKey(movie)] ?? stateKey(movie)}`}
      onClose={onClose}
      size="xl"
    >
      <div className="space-y-5">
        <div className="flex flex-col gap-4 sm:flex-row">
          <Poster
            title={metadata.title}
            poster={metadata.poster}
            className="h-44 w-30 shrink-0 self-start rounded-lg"
          />
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="font-heading text-lg font-semibold">{metadata.title || 'Untitled'}</h2>
              <StatusBadge movie={movie} />
              {!movie.monitored && <Badge variant="outline">Unmonitored</Badge>}
            </div>
            <p className="text-sm text-muted-foreground">
              {metadata.year > 0 ? metadata.year : 'Year unknown'}
              {runtime ? ` · ${runtime}` : ''}
              {metadata.certification ? ` · ${metadata.certification}` : ''}
              {` · Added ${formatDate(Date.parse(movie.addedAt) || null)}`}
            </p>
            <p className="text-sm">
              {rating ? (
                <span className="inline-flex items-center gap-1.5">
                  <StarIcon className="size-4 text-amber-300" aria-hidden="true" />
                  <span className="font-medium">{rating}</span>
                  <span className="text-muted-foreground">
                    {metadata.votes > 0 ? `from ${metadata.votes.toLocaleString()} votes` : 'IMDb rating'}
                  </span>
                </span>
              ) : (
                <span className="text-muted-foreground">IMDb rating unknown</span>
              )}
            </p>
            {metadata.imdbId && (
              <a
                href={`https://www.imdb.com/title/${encodeURIComponent(metadata.imdbId)}/`}
                target="_blank"
                rel="noreferrer"
                className="text-xs text-primary underline-offset-4 hover:underline"
              >
                {metadata.imdbId}
              </a>
            )}
            {metadata.plot && <p className="text-sm text-muted-foreground">{metadata.plot}</p>}
            {movie.error && (
              <p role="alert" className="text-sm text-destructive">
                {movie.error}
              </p>
            )}
          </div>
        </div>

        <dl className="grid gap-2 sm:grid-cols-2">
          <MetaRow label="Genres">{strings(metadata.genres).join(', ') || 'Unknown'}</MetaRow>
          <MetaRow label="Released">{formatDate(releasedTimestamp(movie))}</MetaRow>
          <MetaRow label="Directors">{strings(metadata.directors).join(', ') || 'Unknown'}</MetaRow>
          <MetaRow label="Languages">{strings(metadata.languages).join(', ') || 'Unknown'}</MetaRow>
          <MetaRow label="Cast">{strings(metadata.cast).join(', ') || 'Unknown'}</MetaRow>
          <MetaRow label="Countries">{strings(metadata.countries).join(', ') || 'Unknown'}</MetaRow>
        </dl>

        <Section
          title="Files"
          action={<span className="text-xs text-muted-foreground">{movie.lastSearchAt ? `Last search ${formatAge(movie.lastSearchAt)}` : 'Never searched'}</span>}
        >
          {files.length === 0 ? (
            <EmptyState>No file imported yet.{canWrite ? ' Search releases to download this movie.' : ''}</EmptyState>
          ) : (
            <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
              {files.map((file) => (
                <li key={`${file.rootId}-${file.path}`} className="flex flex-wrap items-center gap-2 p-2.5">
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-2 text-xs break-all">
                      {file.path}
                      {file.missing && <Badge variant="destructive">Missing</Badge>}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {formatBytes(file.size)}
                      {file.quality ? ` · ${file.quality}` : ''}
                      {file.importedAt ? ` · imported ${formatAge(file.importedAt)}` : ''}
                    </p>
                  </div>
                  {file.missing ? (
                    <span className="text-xs text-muted-foreground">
                      File not found on disk. Import a replacement to restore playback.
                    </span>
                  ) : (
                    <>
                      <Button asChild size="sm" variant="outline">
                        <a
                          href={moviesApi.fileUrl(movie.id, file.path)}
                          target="_blank"
                          rel="noreferrer"
                          aria-label={`Play ${file.path}`}
                        >
                          <PlayIcon data-icon="inline-start" />
                          Play
                        </a>
                      </Button>
                      <Button asChild size="sm" variant="ghost">
                        <a
                          href={moviesApi.fileUrl(movie.id, file.path)}
                          download
                          aria-label={`Download ${file.path}`}
                        >
                          <FileDownIcon data-icon="inline-start" />
                          Download
                        </a>
                      </Button>
                    </>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Section>

        <Section
          title="Monitoring and organization"
          action={
            <span className="text-xs text-muted-foreground" role="status">
              {dirty ? 'Unsaved changes' : 'Saved'}
            </span>
          }
        >
          {canWrite ? (
            <>
              <div className="grid gap-4 sm:grid-cols-2">
                <Checkbox
                  id="detail-monitored"
                  label="Monitored"
                  description="Search for releases and upgrades automatically."
                  checked={form.monitored}
                  onChange={(monitored) => setForm({ ...form, monitored })}
                />
                {canReadSettings && (
                  <div className="space-y-2">
                    <label htmlFor="detail-profile" className="text-sm font-medium">
                      Quality profile
                    </label>
                    <Select
                      id="detail-profile"
                      className="h-9 w-full"
                      value={form.profileId}
                      onChange={(event) => setForm({ ...form, profileId: event.target.value })}
                    >
                      <option value="">Default</option>
                      {profiles.map((profile) => (
                        <option key={profile.id} value={profile.id}>
                          {profile.name}
                        </option>
                      ))}
                    </Select>
                  </div>
                )}
                {canReadSettings && (
                  <div className="space-y-2">
                    <label htmlFor="detail-root" className="text-sm font-medium">
                      Root folder
                    </label>
                    <Select
                      id="detail-root"
                      className="h-9 w-full"
                      value={form.rootId}
                      onChange={(event) => setForm({ ...form, rootId: event.target.value })}
                    >
                      <option value="">Default</option>
                      {roots.map((root) => (
                        <option key={root.id || root.path} value={root.id}>
                          {root.path}
                        </option>
                      ))}
                    </Select>
                  </div>
                )}
                <div className="space-y-2">
                  <label htmlFor="detail-tags" className="text-sm font-medium">
                    Tags
                  </label>
                  <Input
                    id="detail-tags"
                    value={form.tags}
                    placeholder="4k, kids"
                    onChange={(event) => setForm({ ...form, tags: event.target.value })}
                  />
                </div>
                <div className="space-y-2 sm:col-span-2">
                  <label htmlFor="detail-collection" className="text-sm font-medium">
                    Collection
                  </label>
                  <Input
                    id="detail-collection"
                    value={form.collection}
                    placeholder="The Matrix Collection"
                    onChange={(event) => setForm({ ...form, collection: event.target.value })}
                  />
                </div>
              </div>
              {!canReadSettings ? (
                <p className="flex items-center gap-2 text-xs text-muted-foreground">
                  <CircleAlertIcon className="size-4 shrink-0" />
                  The current root folder and quality profile stay as configured on the server.
                </p>
              ) : (
                (profiles.length === 0 || roots.length === 0) && (
                  <p className="flex items-center gap-2 text-xs text-amber-300">
                    <CircleAlertIcon className="size-4 shrink-0" />
                    {roots.length === 0 ? (
                      <>
                        Add a root folder in{' '}
                        <a href="#storage" className="underline underline-offset-4">
                          Storage & Paths
                        </a>
                        .
                      </>
                    ) : (
                      'Create a quality profile in the Profiles tab.'
                    )}
                  </p>
                )
              )}
              <div className="flex flex-wrap items-center justify-end gap-2">
                <Button size="sm" disabled={!dirty || saving} onClick={() => void save()}>
                  {saving ? (
                    <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <SaveIcon data-icon="inline-start" />
                  )}
                  Save changes
                </Button>
              </div>
            </>
          ) : (
            <dl className="grid gap-2 sm:grid-cols-2">
              <MetaRow label="Monitoring">{movie.monitored ? 'Monitored' : 'Unmonitored'}</MetaRow>
              <MetaRow label="Quality profile">
                {canReadSettings ? profileName(profiles, movie.profileId) : movie.profileId ? 'Configured profile' : 'Default'}
              </MetaRow>
              <MetaRow label="Root folder">
                {canReadSettings
                  ? roots.find((root) => root.id === movie.rootId)?.path || movie.rootId || 'Default'
                  : movie.rootId
                    ? 'Configured root folder'
                    : 'Default'}
              </MetaRow>
              <MetaRow label="Tags">{tagsOf(movie).join(', ') || 'None'}</MetaRow>
              <MetaRow label="Collection">{movie.collection || 'None'}</MetaRow>
              <p className="text-xs text-muted-foreground sm:col-span-2">
                Editing movie settings requires library write access.
              </p>
            </dl>
          )}
        </Section>

        {canWrite && (
          <Section title="Actions">
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" disabled={refreshing} onClick={() => void refresh()}>
              {refreshing ? (
                <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
              ) : (
                <RefreshCwIcon data-icon="inline-start" />
              )}
              Refresh metadata
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={renaming !== null}
              onClick={() => void runRename(true)}
            >
              {renaming === 'preview' ? (
                <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
              ) : (
                <PencilIcon data-icon="inline-start" />
              )}
              Rename preview
            </Button>
            {confirmRemove ? (
              <span className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-1.5">
                <span className="text-sm text-muted-foreground">Remove from catalog? Files stay on disk.</span>
                <Button size="sm" variant="outline" onClick={() => setConfirmRemove(false)}>
                  Cancel
                </Button>
                <Button size="sm" variant="destructive" disabled={removing} onClick={() => void remove()}>
                  {removing ? (
                    <LoaderCircleIcon className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <Trash2Icon />
                  )}
                  Remove
                </Button>
              </span>
            ) : (
              <Button size="sm" variant="outline" className="text-destructive" onClick={() => setConfirmRemove(true)}>
                <Trash2Icon data-icon="inline-start" />
                Remove from catalog
              </Button>
            )}
          </div>

          {renameResult && (
            <div className="space-y-2 rounded-lg border border-border p-3">
              <p className="text-sm font-medium">
                {renameResult.applied ? 'Renamed files' : 'Rename preview'}
              </p>
              {renameResult.files.length === 0 ? (
                <p className="text-xs text-muted-foreground">No files to rename.</p>
              ) : (
                <ul className="space-y-1 text-xs">
                  {renameResult.files.map((file, index) => (
                    <li key={`${file.from}-${index}`} className="break-all">
                      <span className="text-muted-foreground">{file.from}</span>
                      {' → '}
                      <span>{file.to}</span>
                    </li>
                  ))}
                </ul>
              )}
              {!renameResult.applied && renameResult.files.length > 0 && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={renaming !== null}
                  onClick={() => void runRename(false)}
                >
                  {renaming === 'apply' ? (
                    <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <CheckIcon data-icon="inline-start" />
                  )}
                  Apply rename
                </Button>
              )}
            </div>
          )}
          </Section>
        )}

        {canWrite && (
          <Section
            title="Releases"
          action={
            <Button size="sm" variant="outline" disabled={searching} onClick={() => void search()}>
              {searching ? (
                <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
              ) : (
                <SearchIcon data-icon="inline-start" />
              )}
              {searching ? 'Searching…' : 'Search releases'}
            </Button>
          }
        >
          {releases === null ? (
            <p className="text-sm text-muted-foreground">
              Search the indexer for releases. Rejections show the reason before you grab.
            </p>
          ) : releases.length === 0 ? (
            <EmptyState>No releases found for this movie.</EmptyState>
          ) : (
            <ul className="space-y-2">
              {releases.map((release) => {
                const decision = release.decision
                const chips = [
                  decision.details.quality,
                  decision.details.resolution ? `${decision.details.resolution}p` : '',
                  decision.details.source,
                  decision.details.codec,
                  decision.details.audio,
                  decision.details.hdr,
                  decision.details.group,
                  decision.details.edition,
                  decision.details.proper ? 'Proper' : '',
                  decision.details.language,
                ].filter(Boolean)
                return (
                  <li key={release.id} className="rounded-lg border border-border p-3">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0">
                        <p className="text-sm font-medium break-words">{release.title}</p>
                        <p className="text-xs text-muted-foreground">
                          {formatBytes(release.size)} ·{' '}
                          <time dateTime={release.published} title={formatDateTime(release.published)}>
                            {formatAge(release.published)}
                          </time>
                          {release.imdbId ? ` · ${release.imdbId}` : ''}
                          {release.source ? ` · ${release.source}` : ''}
                          {release.protocol === 'torrent' && release.seeders !== undefined ? ` · ${release.seeders} seeders` : ''}
                        </p>
                      </div>
                      <div className="flex items-center gap-2">
                        <Badge variant="outline">{release.protocol === 'torrent' ? 'Torrent' : 'Usenet'}</Badge>
                        {decision.upgrade && <Badge variant="outline">Upgrade</Badge>}
                        <Badge
                          variant={decision.allowed ? 'outline' : 'destructive'}
                          className={cn(decision.allowed && 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300')}
                        >
                          {decision.allowed ? 'Allowed' : 'Rejected'}
                        </Badge>
                      </div>
                    </div>
                    <div className="mt-2 flex flex-wrap gap-1.5">
                      {chips.map((chip) => (
                        <Badge key={chip} variant="outline" className="text-muted-foreground">
                          {chip}
                        </Badge>
                      ))}
                      <Badge variant="outline" className="text-muted-foreground">
                        Score {decision.score}
                      </Badge>
                    </div>
                    {strings(decision.reasons).length > 0 && (
                      <ul className="mt-2 list-disc space-y-0.5 pl-4 text-xs text-muted-foreground">
                        {strings(decision.reasons).map((reason, index) => (
                          <li key={`${reason}-${index}`}>{reason}</li>
                        ))}
                      </ul>
                    )}
                    <div className="mt-3">
                      <Button
                        size="sm"
                        variant={decision.allowed ? 'default' : 'outline'}
                        disabled={grabbing === release.id}
                        onClick={() => void grab(release)}
                      >
                        {grabbing === release.id ? (
                          <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                        ) : (
                          <DownloadIcon data-icon="inline-start" />
                        )}
                        {decision.allowed ? 'Download' : 'Override & download'}
                      </Button>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
          </Section>
        )}

        <Section title="History">
          {historyError ? (
            <p role="alert" className="text-sm text-destructive">
              {historyError}
            </p>
          ) : history === null ? (
            <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
              <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
              Loading history…
            </p>
          ) : history.length === 0 ? (
            <EmptyState>No history for this movie yet.</EmptyState>
          ) : (
            <ul className="flex flex-col divide-y divide-border">
              {history.map((entry) => (
                <li key={entry.id} className="flex flex-wrap items-start gap-2 py-2 first:pt-0 last:pb-0">
                  <Badge variant="outline" className="capitalize">
                    {entry.type || 'event'}
                  </Badge>
                  <span className="min-w-0 flex-1 text-sm break-words">{entry.message}</span>
                  <time
                    dateTime={entry.createdAt}
                    title={formatDateTime(entry.createdAt)}
                    className="text-xs text-muted-foreground"
                  >
                    {formatAge(entry.createdAt)}
                  </time>
                </li>
              ))}
            </ul>
          )}
        </Section>

        {error && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </p>
        )}
        {notice && (
          <p role="status" className="rounded-md border border-emerald-500/20 bg-emerald-500/5 px-3 py-2 text-sm text-emerald-400">
            <CheckIcon className="mr-2 inline size-4" />
            {notice}
          </p>
        )}
      </div>
    </DialogShell>
  )
}
type ManualFields = {
  title: string
  year: string
  imdbId: string
  released: string
  runtime: string
  certification: string
  genres: string
  directors: string
  cast: string
  poster: string
  plot: string
}

function manualTitle(fields: ManualFields): Title {
  return {
    imdbId: fields.imdbId.trim(),
    title: fields.title.trim(),
    year: Number(fields.year) || 0,
    type: 'movie',
    released: fields.released.trim(),
    rating: null,
    votes: 0,
    runtime: Number(fields.runtime) || 0,
    directors: splitList(fields.directors),
    cast: splitList(fields.cast),
    genres: splitList(fields.genres),
    languages: [],
    countries: [],
    certification: fields.certification.trim(),
    poster: fields.poster.trim(),
    plot: fields.plot.trim(),
  }
}

function AddMovieDialog({
  profiles,
  roots,
  canReadSettings,
  onClose,
  onAdded,
}: {
  profiles: MovieProfile[]
  roots: RootFolder[]
  canReadSettings: boolean
  onClose: () => void
  onAdded: (input: AddMovieInput) => Promise<Movie>
}) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Title[] | null>(null)
  const [page, setPage] = useState(1)
  const [busy, setBusy] = useState<'search' | 'add' | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [manual, setManual] = useState(false)
  const [imdbId, setImdbId] = useState('')
  const [options, setOptions] = useState({
    monitored: true,
    profileId: profiles[0]?.id ?? '',
    rootId: roots[0]?.id ?? '',
    tags: '',
    collection: '',
  })
  const [fields, setFields] = useState<ManualFields>({
    title: '',
    year: '',
    imdbId: '',
    released: '',
    runtime: '',
    certification: '',
    genres: '',
    directors: '',
    cast: '',
    poster: '',
    plot: '',
  })
  const controller = useRef<AbortController | null>(null)

  useEffect(() => () => controller.current?.abort(), [])

  const base = {
    monitored: options.monitored,
    profileId: options.profileId,
    rootId: options.rootId,
    tags: splitList(options.tags),
    collection: options.collection.trim(),
  }

  const search = async (nextPage: number) => {
    const term = query.trim()
    if (!term) return
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    setBusy('search')
    setError('')
    setNotice('')
    try {
      const found = await moviesApi.discover(term, nextPage, request.signal)
      if (request.signal.aborted) return
      setResults(found)
      setPage(nextPage)
    } catch (cause) {
      if (request.signal.aborted) return
      setError(errorMessage(cause))
      setManual(true)
    } finally {
      if (!request.signal.aborted) setBusy(null)
    }
  }

  const addResult = async (candidate: Title) => {
    setBusy('add')
    setError('')
    setNotice('')
    try {
      const movie = await onAdded(
        candidate.imdbId
          ? { imdbId: candidate.imdbId, ...base }
          : { metadata: candidate, ...base },
      )
      setNotice(`Added ${movie.metadata.title || candidate.title}.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const addByImdb = async () => {
    const id = imdbId.trim()
    if (!id) {
      setError('Enter an IMDb ID such as tt0111161.')
      return
    }
    setBusy('add')
    setError('')
    setNotice('')
    try {
      const movie = await onAdded({ imdbId: id, ...base })
      setNotice(`Added ${movie.metadata.title || id}.`)
      setImdbId('')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const addManual = async () => {
    if (!fields.title.trim()) {
      setError('Enter at least a title for a manual entry.')
      return
    }
    setBusy('add')
    setError('')
    setNotice('')
    try {
      const movie = await onAdded({ metadata: manualTitle(fields), ...base })
      setNotice(`Added ${movie.metadata.title || fields.title}.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const field = (key: keyof ManualFields, label: string, props: ComponentProps<'input'> = {}) => (
    <div className="space-y-2">
      <label htmlFor={`manual-${key}`} className="text-sm font-medium">
        {label}
      </label>
      <Input
        id={`manual-${key}`}
        value={fields[key]}
        onChange={(event) => setFields({ ...fields, [key]: event.target.value })}
        {...props}
      />
    </div>
  )

  return (
    <DialogShell
      title="Add movie"
      description="Search the metadata provider, add directly by IMDb ID, or enter details manually."
      onClose={onClose}
      size="xl"
    >
      <div className="space-y-5">
        <div className="grid gap-4 rounded-lg border border-border p-3 sm:grid-cols-2 xl:grid-cols-4">
          <Checkbox
            id="add-monitored"
            label="Monitored"
            checked={options.monitored}
            onChange={(monitored) => setOptions({ ...options, monitored })}
          />
          {canReadSettings && (
            <div className="space-y-2">
              <label htmlFor="add-profile" className="text-sm font-medium">
                Quality profile
              </label>
              <Select
                id="add-profile"
                className="h-9 w-full"
                value={options.profileId}
                onChange={(event) => setOptions({ ...options, profileId: event.target.value })}
              >
                <option value="">Default</option>
                {profiles.map((profile) => (
                  <option key={profile.id} value={profile.id}>
                    {profile.name}
                  </option>
                ))}
              </Select>
            </div>
          )}
          {canReadSettings && (
            <div className="space-y-2">
              <label htmlFor="add-root" className="text-sm font-medium">
                Root folder
              </label>
              <Select
                id="add-root"
                className="h-9 w-full"
                value={options.rootId}
                onChange={(event) => setOptions({ ...options, rootId: event.target.value })}
              >
                <option value="">Default</option>
                {roots.map((root) => (
                  <option key={root.id || root.path} value={root.id}>
                    {root.path}
                  </option>
                ))}
              </Select>
            </div>
          )}
          <div className="space-y-2">
            <label htmlFor="add-tags" className="text-sm font-medium">
              Tags
            </label>
            <Input
              id="add-tags"
              value={options.tags}
              placeholder="4k, kids"
              onChange={(event) => setOptions({ ...options, tags: event.target.value })}
            />
          </div>
          <div className="space-y-2 sm:col-span-2">
            <label htmlFor="add-collection" className="text-sm font-medium">
              Collection
            </label>
            <Input
              id="add-collection"
              value={options.collection}
              placeholder="The Matrix Collection"
              onChange={(event) => setOptions({ ...options, collection: event.target.value })}
            />
          </div>
          {!canReadSettings ? (
            <p className="flex items-center gap-2 text-xs text-muted-foreground sm:col-span-2">
              <CircleAlertIcon className="size-4 shrink-0" />
              Movies use the server's default root folder and quality profile.
            </p>
          ) : (
            roots.length === 0 && (
              <p className="flex items-center gap-2 text-xs text-amber-300 sm:col-span-2">
                <CircleAlertIcon className="size-4 shrink-0" />
                No root folder configured. Add one in{' '}
                <a href="#storage" className="underline underline-offset-4">
                  Storage & Paths
                </a>{' '}
                so imports have a destination.
              </p>
            )
          )}
        </div>

        <section className="space-y-3">
          <h3 className="font-heading text-sm font-semibold">Search metadata</h3>
          <form
            className="flex flex-col gap-2 sm:flex-row"
            onSubmit={(event) => {
              event.preventDefault()
              void search(1)
            }}
          >
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Movie title"
              aria-label="Search movie metadata"
            />
            <Button type="submit" disabled={!query.trim() || busy === 'search'}>
              {busy === 'search' ? (
                <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
              ) : (
                <SearchIcon data-icon="inline-start" />
              )}
              {busy === 'search' ? 'Searching…' : 'Search'}
            </Button>
          </form>
          {results === null ? (
            <p className="text-sm text-muted-foreground">
              Results appear here with year, rating, and poster.
            </p>
          ) : results.length === 0 ? (
            <EmptyState>No metadata results on this page.</EmptyState>
          ) : (
            <>
              <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
                {results.map((candidate) => (
                  <li key={`${candidate.imdbId}-${candidate.title}`} className="flex flex-wrap items-center gap-3 p-3">
                    <Poster
                      title={candidate.title}
                      poster={candidate.poster}
                      className="h-14 w-10 shrink-0 rounded-sm"
                    />
                    <div className="min-w-0 flex-1">
                      <p className="text-sm font-medium">
                        {candidate.title || 'Untitled'}{' '}
                        <span className="font-normal text-muted-foreground">
                          {candidate.year > 0 ? candidate.year : ''}
                        </span>
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {candidate.type || 'movie'}
                        {candidate.imdbId ? ` · ${candidate.imdbId}` : ''}
                        {candidate.rating ? ` · ${candidate.rating.toFixed(1)}/10` : ''}
                      </p>
                    </div>
                    <Button
                      size="sm"
                      disabled={busy !== null}
                      onClick={() => void addResult(candidate)}
                    >
                      {busy === 'add' ? (
                        <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                      ) : (
                        <PlusIcon data-icon="inline-start" />
                      )}
                      Add
                    </Button>
                  </li>
                ))}
              </ul>
              <div className="flex items-center justify-between gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={page <= 1 || busy !== null}
                  onClick={() => void search(page - 1)}
                >
                  <ChevronLeftIcon data-icon="inline-start" />
                  Previous
                </Button>
                <span className="text-xs text-muted-foreground">Page {page}</span>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={results.length === 0 || busy !== null}
                  onClick={() => void search(page + 1)}
                >
                  Next
                  <ChevronRightIcon data-icon="inline-end" />
                </Button>
              </div>
            </>
          )}
        </section>

        <section className="space-y-3 border-t border-border pt-4">
          <h3 className="font-heading text-sm font-semibold">Add by IMDb ID</h3>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              value={imdbId}
              onChange={(event) => setImdbId(event.target.value)}
              placeholder="tt0111161"
              aria-label="IMDb ID"
              className="sm:max-w-xs"
            />
            <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => void addByImdb()}>
              <PlusIcon data-icon="inline-start" />
              Add by IMDb ID
            </Button>
          </div>
        </section>

        <section className="space-y-3 border-t border-border pt-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="font-heading text-sm font-semibold">Manual details</h3>
            <Button size="sm" variant="ghost" onClick={() => setManual((current) => !current)}>
              {manual ? 'Hide manual entry' : 'Enter details manually'}
            </Button>
          </div>
          {manual && (
            <div className="space-y-4">
              <p className="text-xs text-muted-foreground">
                Use this when no metadata provider is configured. Details are stored with the movie.
              </p>
              <div className="grid gap-4 sm:grid-cols-2">
                {field('title', 'Title', { required: true })}
                {field('year', 'Year', { type: 'number', min: 0 })}
                {field('imdbId', 'IMDb ID')}
                {field('released', 'Release date')}
                {field('runtime', 'Runtime (minutes)', { type: 'number', min: 0 })}
                {field('certification', 'Certification')}
                {field('genres', 'Genres', { placeholder: 'Action, Sci-Fi' })}
                {field('directors', 'Directors')}
                {field('cast', 'Cast')}
                {field('poster', 'Poster URL', { type: 'url' })}
                <div className="space-y-2 sm:col-span-2">
                  <label htmlFor="manual-plot" className="text-sm font-medium">
                    Plot
                  </label>
                  <textarea
                    id="manual-plot"
                    rows={3}
                    value={fields.plot}
                    onChange={(event) => setFields({ ...fields, plot: event.target.value })}
                    className="w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/30"
                  />
                </div>
              </div>
              <div className="flex justify-end">
                <Button size="sm" disabled={busy !== null} onClick={() => void addManual()}>
                  <PlusIcon data-icon="inline-start" />
                  Add manual entry
                </Button>
              </div>
            </div>
          )}
        </section>

        {error && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </p>
        )}
        {notice && (
          <p role="status" className="rounded-md border border-emerald-500/20 bg-emerald-500/5 px-3 py-2 text-sm text-emerald-400">
            <CheckIcon className="mr-2 inline size-4" />
            {notice}
          </p>
        )}
      </div>
    </DialogShell>
  )
}

function ScanDialog({
  movies,
  roots,
  importMode,
  onClose,
  onImported,
}: {
  movies: Movie[]
  roots: RootFolder[]
  importMode: string
  onClose: () => void
  onImported: (movie: Movie) => void
}) {
  const [rootId, setRootId] = useState(roots[0]?.id ?? '')
  const [candidates, setCandidates] = useState<ScanCandidate[] | null>(null)
  const [scanning, setScanning] = useState(false)
  const [importing, setImporting] = useState('')
  const [matches, setMatches] = useState<Record<string, string>>({})
  const [imdbIds, setImdbIds] = useState<Record<string, string>>({})
  const [importedPaths, setImportedPaths] = useState<Record<string, boolean>>({})
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const controller = useRef<AbortController | null>(null)

  useEffect(() => () => controller.current?.abort(), [])

  // A matched id means identity only; imported means a catalog file really sits at this path and root.
  const isImported = (candidate: ScanCandidate) =>
    Boolean(importedPaths[candidate.path]) ||
    movies.some((movie) =>
      availableFiles(movie).some((file) => file.rootId === rootId && file.path === candidate.path),
    )

  const scan = async () => {
    if (!rootId) {
      setError('Choose a root folder to scan.')
      return
    }
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    setScanning(true)
    setCandidates(null)
    setMatches({})
    setImdbIds({})
    setImportedPaths({})
    setError('')
    setNotice('')
    try {
      const found = await moviesApi.scan(rootId, request.signal)
      if (!request.signal.aborted) setCandidates(found)
    } catch (cause) {
      if (!request.signal.aborted) setError(errorMessage(cause))
    } finally {
      if (!request.signal.aborted) setScanning(false)
    }
  }

  const importCandidate = async (candidate: ScanCandidate) => {
    const movieId = matches[candidate.path] ?? candidate.matchedMovieId ?? ''
    const imdbId = (imdbIds[candidate.path] ?? candidate.imdbId ?? '').trim()
    if (!movieId && !imdbId) {
      setError('Match this file to a catalog movie or enter an IMDb ID before importing.')
      return
    }
    setImporting(candidate.path)
    setError('')
    setNotice('')
    try {
      const movie = await moviesApi.importFile({
        rootId,
        path: candidate.path,
        movieId: movieId || undefined,
        imdbId: movieId ? undefined : imdbId || undefined,
      })
      onImported(movie)
      setImportedPaths((previous) => ({ ...previous, [candidate.path]: true }))
      setNotice(`Imported ${candidate.path} into ${movie.metadata.title || 'the catalog'}.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setImporting('')
    }
  }

  return (
    <DialogShell
      title="Scan library"
      description="Scanning is read-only. Matching only preselects a catalog movie; choose Import to link the file."
      onClose={onClose}
      size="xl"
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-52 flex-1 space-y-2">
            <label htmlFor="scan-root" className="text-sm font-medium">
              Root folder
            </label>
            <Select
              id="scan-root"
              className="h-9 w-full"
              value={rootId}
              onChange={(event) => setRootId(event.target.value)}
            >
              {roots.length === 0 && <option value="">No root folders configured</option>}
              {roots.map((root) => (
                <option key={root.id || root.path} value={root.id}>
                  {root.path}
                </option>
              ))}
            </Select>
          </div>
          <Button size="sm" disabled={scanning || !rootId} onClick={() => void scan()}>
            {scanning ? (
              <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
            ) : (
              <FolderSearchIcon data-icon="inline-start" />
            )}
            {scanning ? 'Scanning…' : 'Scan folder'}
          </Button>
        </div>

        <p className="text-xs text-muted-foreground">
          Imports follow the configured import mode ({importMode || 'copy'}). Existing files are preserved.
        </p>

        {error && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </p>
        )}
        {notice && (
          <p role="status" className="rounded-md border border-emerald-500/20 bg-emerald-500/5 px-3 py-2 text-sm text-emerald-400">
            <CheckIcon className="mr-2 inline size-4" />
            {notice}
          </p>
        )}

        {candidates === null ? (
          <EmptyState>Choose a root folder and scan to list movie files that are not in the catalog.</EmptyState>
        ) : candidates.length === 0 ? (
          <EmptyState>No movie files found in this folder.</EmptyState>
        ) : (
          <ul className="space-y-2">
            {candidates.map((candidate) => {
              const targetId = matches[candidate.path] ?? candidate.matchedMovieId ?? ''
              const target = movies.find((movie) => movie.id === targetId)
              const imported = isImported(candidate)
              return (
                <li key={candidate.path} className="space-y-2 rounded-lg border border-border p-3">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <p className="text-sm font-medium">
                        {candidate.title || 'Unknown title'}
                        {candidate.year > 0 ? ` (${candidate.year})` : ''}
                      </p>
                      <p className="text-xs break-all text-muted-foreground">{candidate.path}</p>
                      <p className="text-xs text-muted-foreground">
                        {formatBytes(candidate.size)}
                        {candidate.quality ? ` · ${candidate.quality}` : ''}
                        {candidate.imdbId ? ` · ${candidate.imdbId}` : ''}
                      </p>
                    </div>
                    {imported ? (
                      <Badge variant="outline" className="border-emerald-400/25 bg-emerald-400/10 text-emerald-300">
                        Imported
                      </Badge>
                    ) : target ? (
                      <Badge variant="outline">Matches {target.metadata.title}</Badge>
                    ) : (
                      <Badge variant="outline">Unmatched</Badge>
                    )}
                  </div>
                  {candidate.error && <p className="text-xs text-destructive">{candidate.error}</p>}
                  {imported && (
                    <p className="text-xs text-muted-foreground">
                      This file is already in the catalog under this root. Scan again to refresh the list.
                    </p>
                  )}
                  <div className="flex flex-wrap items-end gap-2">
                    <div className="min-w-48 flex-1 space-y-1">
                      <label
                        htmlFor={`scan-movie-${candidate.path}`}
                        className="text-xs text-muted-foreground"
                      >
                        Match to catalog movie
                      </label>
                      <Select
                        id={`scan-movie-${candidate.path}`}
                        className="h-8 w-full"
                        value={targetId}
                        onChange={(event) =>
                          setMatches({ ...matches, [candidate.path]: event.target.value })
                        }
                      >
                        <option value="">No catalog match</option>
                        {movies.map((movie) => (
                          <option key={movie.id} value={movie.id}>
                            {movie.metadata.title}
                            {movie.metadata.year > 0 ? ` (${movie.metadata.year})` : ''}
                          </option>
                        ))}
                      </Select>
                    </div>
                    <div className="min-w-40 space-y-1">
                      <label htmlFor={`scan-imdb-${candidate.path}`} className="text-xs text-muted-foreground">
                        IMDb ID
                      </label>
                      <Input
                        id={`scan-imdb-${candidate.path}`}
                        value={imdbIds[candidate.path] ?? candidate.imdbId ?? ''}
                        placeholder="tt0111161"
                        className="h-8 font-mono text-xs"
                        onChange={(event) => setImdbIds({ ...imdbIds, [candidate.path]: event.target.value })}
                      />
                    </div>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={importing !== '' || imported}
                      onClick={() => void importCandidate(candidate)}
                    >
                      {importing === candidate.path ? (
                        <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                      ) : (
                        <FileDownIcon data-icon="inline-start" />
                      )}
                      Import
                    </Button>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </DialogShell>
  )
}
type Tab = 'library' | 'wanted' | 'calendar' | 'profiles' | 'watchlists' | 'activity'

export function MoviesPage() {
  const { can } = useAuth()
  const canWrite = can(accessPermissions.libraryWrite)
  const canReadSettings = can(accessPermissions.settingsRead)
  const canWriteSettings = can(accessPermissions.settingsWrite)
  const [movies, setMovies] = useState<Movie[] | null>(null)
  const [profiles, setProfiles] = useState<MovieProfile[]>([])
  const [config, setConfig] = useState<MovieConfig | null>(null)
  const [loadError, setLoadError] = useState('')
  const [setupError, setSetupError] = useState('')
  const [tab, setTab] = useState<Tab>('library')
  const [detailId, setDetailId] = useState<string | null>(null)
  const [detailSearch, setDetailSearch] = useState(false)
  const [addOpen, setAddOpen] = useState(false)
  const [scanOpen, setScanOpen] = useState(false)
  const [watched, setWatched] = useState<string[]>([])
  const requestId = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const watchTimers = useRef<number[]>([])
  const rootRef = useRef<HTMLDivElement>(null)

  // App keeps the movies route mounted but hidden; skip background work while it is off screen.
  const pageVisible = useCallback(() => {
    const root = rootRef.current
    return (
      root !== null &&
      document.visibilityState === 'visible' &&
      !root.closest('[hidden]') &&
      !root.closest('[inert]')
    )
  }, [])

  const reload = useCallback(async () => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    const id = requestId.current + 1
    requestId.current = id

    try {
      const list = await moviesApi.list(request.signal)
      if (requestId.current !== id) return
      setMovies(list)
      setLoadError('')
    } catch (cause) {
      if (request.signal.aborted || requestId.current !== id) return
      setLoadError(errorMessage(cause))
    }

    // Profiles and configuration sit behind settings.read; the catalog still loads without them.
    if (!canReadSettings) {
      setProfiles([])
      setConfig(null)
      setSetupError('')
      return
    }

    const [profilesResult, configResult] = await Promise.allSettled([
      moviesApi.profiles(request.signal),
      moviesApi.config(request.signal),
    ])
    if (requestId.current !== id) return
    if (profilesResult.status === 'fulfilled') setProfiles(profilesResult.value)
    if (configResult.status === 'fulfilled') setConfig(configResult.value)
    const failed = [profilesResult, configResult].find((result) => result.status === 'rejected')
    setSetupError(failed ? errorMessage(failed.reason) : '')
  }, [canReadSettings])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void reload()
    return () => controller.current?.abort()
  }, [reload])

  useEffect(() => {
    const refresh = () => void reload()
    window.addEventListener('movies-changed', refresh)
    return () => window.removeEventListener('movies-changed', refresh)
  }, [reload])

  useEffect(
    () => () => {
      watchTimers.current.forEach((timer) => window.clearTimeout(timer))
      watchTimers.current = []
    },
    [],
  )

  const watchMovie = useCallback((id: string) => {
    setWatched((previous) => (previous.includes(id) ? previous : [...previous, id]))
    watchTimers.current.push(
      window.setTimeout(
        () => setWatched((previous) => previous.filter((item) => item !== id)),
        5 * 60_000,
      ),
    )
  }, [])

  const saveMovie = useCallback(async (movie: Movie) => {
    const saved = await moviesApi.update(movie)
    setMovies((list) => list?.map((item) => (item.id === saved.id ? saved : item)) ?? list)
    notifyMoviesChanged()
    return saved
  }, [])

  const removeMovie = useCallback(async (id: string) => {
    await moviesApi.remove(id)
    setMovies((list) => list?.filter((item) => item.id !== id) ?? list)
    notifyMoviesChanged()
  }, [])

  const addMovie = useCallback(async (input: AddMovieInput) => {
    const movie = await moviesApi.add(input)
    setMovies((list) => (list ? [movie, ...list.filter((item) => item.id !== movie.id)] : [movie]))
    notifyMoviesChanged()
    return movie
  }, [])

  const bulkEdit = useCallback(async (input: BulkEditInput) => {
    const updated = await moviesApi.bulkEdit(input)
    const byId = new Map(updated.map((movie) => [movie.id, movie]))
    setMovies((list) => list?.map((item) => byId.get(item.id) ?? item) ?? list)
    notifyMoviesChanged()
  }, [])

  const mergeMovie = useCallback((movie: Movie) => {
    setMovies((list) => list?.map((item) => (item.id === movie.id ? movie : item)) ?? list)
    notifyMoviesChanged()
  }, [])

  const openMovie = useCallback((id: string) => {
    setDetailSearch(false)
    setDetailId(id)
  }, [])

  const searchMovie = useCallback(
    (id: string) => {
      // Release search needs library.write; readers open the details without a search.
      setDetailSearch(canWrite)
      setDetailId(id)
    },
    [canWrite],
  )

  const live = useMemo(() => (movies ?? []).some((movie) => activeStatuses.has(stateKey(movie))), [movies])
  const shouldPoll = live || watched.length > 0

  useEffect(() => {
    if (!shouldPoll) return
    let wake: number | undefined
    const tick = () => {
      if (pageVisible()) void reload()
    }
    const onWake = () => {
      window.clearTimeout(wake)
      wake = window.setTimeout(tick, 0)
    }
    const timer = window.setInterval(tick, watchedIntervalMs)
    document.addEventListener('visibilitychange', onWake)
    window.addEventListener('hashchange', onWake)
    return () => {
      window.clearTimeout(wake)
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onWake)
      window.removeEventListener('hashchange', onWake)
    }
  }, [pageVisible, reload, shouldPoll])

  const roots = config?.rootFolders ?? []
  const detailMovie = detailId ? ((movies ?? []).find((movie) => movie.id === detailId) ?? null) : null
  const wantedCount = (movies ?? []).filter(
    (movie) => movie.monitored && availableFiles(movie).length === 0,
  ).length

  const tabs: { id: Tab; label: string; badge?: number }[] = [
    { id: 'library', label: 'Library' },
    { id: 'wanted', label: 'Wanted', badge: wantedCount },
    { id: 'calendar', label: 'Calendar' },
  ]
  if (canReadSettings) {
    tabs.push({ id: 'profiles', label: 'Profiles', badge: profiles.length })
    tabs.push({ id: 'watchlists', label: 'Watchlists' })
  }
  tabs.push({ id: 'activity', label: 'Activity' })
  // A tab can disappear when permissions change; fall back instead of rendering an empty panel.
  const activeTab = tabs.some((item) => item.id === tab) ? tab : 'library'

  const onTabKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
    event.preventDefault()
    const index = tabs.findIndex((item) => item.id === activeTab)
    const offset = event.key === 'ArrowRight' ? 1 : -1
    setTab(tabs[(index + offset + tabs.length) % tabs.length].id)
  }

  return (
    <div ref={rootRef} className="flex flex-col gap-6">
      <PageHeading
        title="Movies"
        description="Search, organize, and monitor your movie catalog."
        action={
          <div className="flex flex-wrap gap-2">
            <Button asChild size="sm" variant="outline">
              <a href="#usenet">View queue</a>
            </Button>
            {canWrite && (
              <Button size="sm" onClick={() => setAddOpen(true)}>
                <PlusIcon data-icon="inline-start" />
                Add movie
              </Button>
            )}
          </div>
        }
      />

      {loadError && movies !== null && (
        <ErrorNote onRetry={() => void reload()}>
          Could not refresh the catalog. {loadError}
        </ErrorNote>
      )}
      {setupError && (
        <ErrorNote onRetry={() => void reload()}>
          Some movie settings could not be loaded. {setupError}
        </ErrorNote>
      )}

      {movies === null && !loadError && (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
          Loading movie catalog…
        </p>
      )}
      {movies === null && loadError && (
        <ErrorNote onRetry={() => void reload()}>Could not load the movie catalog. {loadError}</ErrorNote>
      )}

      {movies !== null && (
        <>
          <div
            role="tablist"
            aria-label="Movie sections"
            onKeyDown={onTabKeyDown}
            className="flex flex-wrap gap-1 border-b border-border"
          >
            {tabs.map((item) => (
              <button
                key={item.id}
                type="button"
                role="tab"
                id={`movies-tab-${item.id}`}
                aria-selected={activeTab === item.id}
                aria-controls="movies-panel"
                tabIndex={activeTab === item.id ? 0 : -1}
                onClick={() => setTab(item.id)}
                className={cn(
                  '-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
                  activeTab === item.id
                    ? 'border-primary text-foreground'
                    : 'border-transparent text-muted-foreground hover:text-foreground',
                )}
              >
                {item.label}
                {item.badge !== undefined && item.badge > 0 && (
                  <span className="rounded-full bg-muted px-1.5 text-[11px] font-semibold tabular-nums">
                    {item.badge}
                  </span>
                )}
              </button>
            ))}
          </div>

          <div role="tabpanel" id="movies-panel" aria-labelledby={`movies-tab-${activeTab}`}>
            <div hidden={activeTab !== 'library'}>
              <LibraryView
                movies={movies}
                profiles={profiles}
                roots={roots}
                canWrite={canWrite}
                canReadSettings={canReadSettings}
                onOpenMovie={openMovie}
                onAdd={() => setAddOpen(true)}
                onScan={() => setScanOpen(true)}
                onBulkEdit={bulkEdit}
              />
            </div>
            {activeTab === 'wanted' && <WantedList movies={movies} canWrite={canWrite} onOpenMovie={searchMovie} />}
            {activeTab === 'calendar' && <CalendarTab onOpenMovie={openMovie} />}
            {activeTab === 'profiles' && (
              <QualityProfilesTab profiles={profiles} canWrite={canWriteSettings} onChanged={() => void reload()} />
            )}
            {activeTab === 'watchlists' && <WatchlistsTab profiles={profiles} roots={roots} canWrite={canWriteSettings} />}
            {activeTab === 'activity' && <ActivityTab movies={movies} onOpenMovie={openMovie} />}
          </div>
        </>
      )}

      {canWrite && addOpen && (
        <AddMovieDialog
          profiles={profiles}
          roots={roots}
          canReadSettings={canReadSettings}
          onClose={() => setAddOpen(false)}
          onAdded={addMovie}
        />
      )}

      {canWrite && canReadSettings && scanOpen && (
        <ScanDialog
          movies={movies ?? []}
          roots={roots}
          importMode={config?.importMode ?? ''}
          onClose={() => setScanOpen(false)}
          onImported={mergeMovie}
        />
      )}

      {detailMovie && (
        <MovieDetailDialog
          key={detailMovie.id}
          movie={detailMovie}
          profiles={profiles}
          roots={roots}
          canWrite={canWrite}
          canReadSettings={canReadSettings}
          autoSearch={detailSearch}
          onClose={() => {
            setDetailId(null)
            setDetailSearch(false)
          }}
          onSave={saveMovie}
          onRemove={removeMovie}
          onRefreshed={mergeMovie}
          onGrabbed={watchMovie}
          onChanged={notifyMoviesChanged}
        />
      )}
    </div>
  )
}
