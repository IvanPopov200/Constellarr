import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import {
  ArrowDownIcon,
  ArrowUpIcon,
  CalendarIcon,
  CircleAlertIcon,
  FolderSearchIcon,
  LayoutGridIcon,
  ListIcon,
  ListFilterIcon,
  LoaderCircleIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  StarIcon,
  XIcon,
} from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { AddSeriesDialog } from '@/components/tv-add-dialog'
import { SeriesDetailDialog } from '@/components/tv-detail-dialog'
import { ReleaseDialog } from '@/components/tv-release-dialog'
import { ScanDialog } from '@/components/tv-scan-dialog'
import {
  EmptyState,
  ErrorNote,
  LoadingNote,
  Notice,
  Poster,
  Progress,
  Section,
  Select,
  SeriesStatusBadge,
  StatusBadge,
} from '@/components/tv-ui'
import { errorMessage } from '@/lib/api'
import { formatAge } from '@/lib/format'
import { moviesApi, type MovieProfile } from '@/lib/movies-api'
import { QualityProfilesTab } from '@/components/movies-page'
import {
  activeStatuses,
  airDateValue,
  bestFile,
  detailFetchLimit,
  formatDate,
  formatDateTime,
  monitorModes,
  ratingText,
  seasonLabel,
  seriesNeedsAttention,
  splitList,
  stateKey,
  statusLabel,
  strings,
  tagsOf,
  todayStart,
  wantedRows,
} from '@/components/tv-shared'
import {
  tvApi,
  type AddSeriesInput,
  type BulkSeriesInput,
  type CalendarEntry,
  type RootFolder,
  type Series,
  type Target,
  type TvConfig,
  type TvHistoryEntry,
} from '@/lib/tv-api'
import { cn } from 'cn'

type Tab = 'library' | 'wanted' | 'calendar' | 'activity' | 'profiles'

type SortField = 'title' | 'year' | 'rating' | 'added' | 'progress' | 'next'

const sortFields: { value: SortField; label: string }[] = [
  { value: 'title', label: 'Title' },
  { value: 'year', label: 'Year' },
  { value: 'rating', label: 'IMDb rating' },
  { value: 'added', label: 'Date added' },
  { value: 'progress', label: 'Progress' },
  { value: 'next', label: 'Next air date' },
]

function matchesSearch(series: Series, term: string) {
  const metadata = series.metadata
  return [
    metadata.title,
    metadata.imdbId,
    metadata.year > 0 ? String(metadata.year) : '',
    metadata.certification,
    metadata.plot,
    ...strings(metadata.genres),
    ...strings(metadata.directors),
    ...strings(metadata.cast),
    ...tagsOf(series),
  ]
    .join(' ')
    .toLowerCase()
    .includes(term)
}

function sortValue(series: Series, field: SortField, nextAirdates: Map<string, number>) {
  switch (field) {
    case 'title':
      return series.metadata.title || null
    case 'year':
      return series.metadata.year > 0 ? series.metadata.year : null
    case 'rating':
      return typeof series.metadata.rating === 'number' && series.metadata.rating > 0
        ? series.metadata.rating
        : null
    case 'added': {
      const value = Date.parse(series.addedAt)
      return Number.isFinite(value) ? value : null
    }
    case 'progress':
      return series.total > 0 ? series.downloaded / series.total : null
    case 'next':
      return nextAirdates.get(series.id) ?? null
  }
}

// Unknown values stay last in both directions.
function compareSeries(a: Series, b: Series, field: SortField, ascending: boolean, nextAirdates: Map<string, number>) {
  const left = sortValue(a, field, nextAirdates)
  const right = sortValue(b, field, nextAirdates)
  if (left === null && right === null) return 0
  if (left === null) return 1
  if (right === null) return -1
  const result =
    typeof left === 'number' && typeof right === 'number'
      ? left - right
      : String(left).localeCompare(String(right), undefined, { sensitivity: 'base', numeric: true })
  return ascending ? result : -result
}

function LibraryView({
  series,
  profiles,
  roots,
  nextAirdates,
  onOpen,
  onAdd,
  onScan,
  onBulk,
}: {
  series: Series[]
  profiles: MovieProfile[]
  roots: RootFolder[]
  nextAirdates: Map<string, number>
  onOpen: (id: string) => void
  onAdd: () => void
  onScan: () => void
  onBulk: (input: BulkSeriesInput) => Promise<void>
}) {
  const [query, setQuery] = useState('')
  const [monitor, setMonitor] = useState('all')
  const [status, setStatus] = useState('all')
  const [genre, setGenre] = useState('all')
  const [cast, setCast] = useState('all')
  const [ratingMin, setRatingMin] = useState('')
  const [ratingMax, setRatingMax] = useState('')
  const [profile, setProfile] = useState('all')
  const [sort, setSort] = useState<SortField>('added')
  const [ascending, setAscending] = useState(false)
  const [view, setView] = useState<'grid' | 'table'>('grid')
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const [bulk, setBulk] = useState({
    monitored: 'keep',
    monitorMode: 'keep',
    profileId: 'keep',
    rootId: 'keep',
    tags: '',
    clearTags: false,
  })
  const [bulkBusy, setBulkBusy] = useState(false)
  const [bulkError, setBulkError] = useState('')
  const [bulkNotice, setBulkNotice] = useState('')
  const [filtersOpen, setFiltersOpen] = useState(false)

  const statusOptions = useMemo(() => [...new Set(series.map((item) => stateKey(item)))].sort(), [series])
  const genreOptions = useMemo(
    () => [...new Set(series.flatMap((item) => strings(item.metadata.genres)))].sort((a, b) => a.localeCompare(b)),
    [series],
  )
  const castOptions = useMemo(
    () => [...new Set(series.flatMap((item) => strings(item.metadata.cast)))].sort((a, b) => a.localeCompare(b)),
    [series],
  )

  const filterCount =
    (monitor !== 'all' ? 1 : 0) +
    (status !== 'all' ? 1 : 0) +
    (genre !== 'all' ? 1 : 0) +
    (cast !== 'all' ? 1 : 0) +
    (profile !== 'all' ? 1 : 0) +
    (ratingMin.trim() ? 1 : 0) +
    (ratingMax.trim() ? 1 : 0)

  const clearFilters = () => {
    setMonitor('all')
    setStatus('all')
    setGenre('all')
    setCast('all')
    setProfile('all')
    setRatingMin('')
    setRatingMax('')
  }

  const visible = useMemo(() => {
    const term = query.trim().toLowerCase()
    const min = ratingMin.trim() === '' ? null : Number(ratingMin)
    const max = ratingMax.trim() === '' ? null : Number(ratingMax)
    return series
      .filter((item) => {
        const rating = ratingText(item.metadata.rating)
        return (
          (monitor === 'all' || (monitor === 'monitored') === item.monitored) &&
          (status === 'all' || stateKey(item) === status) &&
          (genre === 'all' || strings(item.metadata.genres).includes(genre)) &&
          (cast === 'all' || strings(item.metadata.cast).includes(cast)) &&
          (profile === 'all' || item.profileId === profile) &&
          (min === null || !Number.isFinite(min) || (rating !== null && Number(rating) >= min)) &&
          (max === null || !Number.isFinite(max) || (rating !== null && Number(rating) <= max)) &&
          (!term || matchesSearch(item, term))
        )
      })
      .sort((a, b) => compareSeries(a, b, sort, ascending, nextAirdates))
  }, [series, query, monitor, status, genre, cast, profile, ratingMin, ratingMax, sort, ascending, nextAirdates])

  const selected = useMemo(() => {
    const ids = new Set(series.map((item) => item.id))
    return new Set([...selectedIds].filter((id) => ids.has(id)))
  }, [selectedIds, series])

  const toggle = (id: string) =>
    setSelectedIds((previous) => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const applyBulk = async () => {
    if (selected.size === 0) return
    const input: BulkSeriesInput = { ids: [...selected] }
    if (bulk.monitored !== 'keep') input.monitored = bulk.monitored === 'monitored'
    if (bulk.monitorMode !== 'keep') input.monitorMode = bulk.monitorMode
    if (bulk.profileId !== 'keep') input.profileId = bulk.profileId
    if (bulk.rootId !== 'keep') input.rootId = bulk.rootId
    if (bulk.clearTags) input.tags = []
    else if (bulk.tags.trim()) input.tags = splitList(bulk.tags)
    if (Object.keys(input).length === 1) {
      setBulkError('Choose at least one change to apply.')
      return
    }
    setBulkBusy(true)
    setBulkError('')
    setBulkNotice('')
    try {
      await onBulk(input)
      setBulkNotice(`Updated ${selected.size} series.`)
      setSelectedIds(new Set())
      setBulk({ monitored: 'keep', monitorMode: 'keep', profileId: 'keep', rootId: 'keep', tags: '', clearTags: false })
    } catch (cause) {
      setBulkError(errorMessage(cause))
    } finally {
      setBulkBusy(false)
    }
  }

  if (series.length === 0) {
    return (
      <EmptyState>
        <div className="space-y-3">
          <p className="text-sm">
            Your TV library is empty. Add a series, or scan an existing folder to match files to episodes.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={onAdd}>
              <PlusIcon data-icon="inline-start" />
              Add series
            </Button>
            <Button size="sm" variant="outline" onClick={onScan}>
              <FolderSearchIcon data-icon="inline-start" />
              Scan library
            </Button>
          </div>
        </div>
      </EmptyState>
    )
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
            aria-label="Search series"
            className="pl-8"
          />
        </div>
        <div className="ml-auto flex items-center gap-2">
          <label htmlFor="tv-sort" className="text-sm text-muted-foreground">
            Sort
          </label>
          <Select id="tv-sort" value={sort} onChange={(event) => setSort(event.target.value as SortField)}>
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
        </div>
      </div>

      <div className="flex items-center justify-between sm:hidden">
        <span className="text-sm text-muted-foreground" role="status">{visible.length} series</span>
        <Button size="sm" variant="outline" aria-expanded={filtersOpen} aria-controls="tv-library-filters" onClick={() => setFiltersOpen((open) => !open)}>
          <ListFilterIcon data-icon="inline-start" />
          Filters{filterCount > 0 ? ` (${filterCount})` : ''}
        </Button>
      </div>
      <div id="tv-library-filters" className={cn(filtersOpen ? 'grid' : 'hidden', 'gap-2 rounded-lg border border-border p-3 sm:grid sm:grid-cols-2 lg:grid-cols-4')}>
        <div className="space-y-1.5">
          <label htmlFor="tv-filter-monitor" className="text-xs font-medium text-muted-foreground">
            Monitoring
          </label>
          <Select
            id="tv-filter-monitor"
            className="w-full"
            value={monitor}
            onChange={(event) => setMonitor(event.target.value)}
          >
            <option value="all">All monitoring</option>
            <option value="monitored">Monitored</option>
            <option value="unmonitored">Unmonitored</option>
          </Select>
        </div>
        <div className="space-y-1.5">
          <label htmlFor="tv-filter-status" className="text-xs font-medium text-muted-foreground">
            Status
          </label>
          <Select
            id="tv-filter-status"
            className="w-full"
            value={status}
            onChange={(event) => setStatus(event.target.value)}
          >
            <option value="all">All statuses</option>
            {statusOptions.map((value) => (
              <option key={value} value={value}>
                {statusLabel(value)}
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label htmlFor="tv-filter-genre" className="text-xs font-medium text-muted-foreground">
            Genre
          </label>
          <Select id="tv-filter-genre" className="w-full" value={genre} onChange={(event) => setGenre(event.target.value)}>
            <option value="all">Any genre</option>
            {genreOptions.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label htmlFor="tv-filter-cast" className="text-xs font-medium text-muted-foreground">
            Cast
          </label>
          <Select id="tv-filter-cast" className="w-full" value={cast} onChange={(event) => setCast(event.target.value)}>
            <option value="all">Any cast member</option>
            {castOptions.map((value) => (
              <option key={value} value={value}>
                {value}
              </option>
            ))}
          </Select>
        </div>
        <div className="space-y-1.5">
          <label htmlFor="tv-filter-rating-min" className="text-xs font-medium text-muted-foreground">
            IMDb rating min
          </label>
          <Input
            id="tv-filter-rating-min"
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
          <label htmlFor="tv-filter-rating-max" className="text-xs font-medium text-muted-foreground">
            IMDb rating max
          </label>
          <Input
            id="tv-filter-rating-max"
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
        <div className="space-y-1.5">
          <label htmlFor="tv-filter-profile" className="text-xs font-medium text-muted-foreground">
            Quality profile
          </label>
          <Select
            id="tv-filter-profile"
            className="w-full"
            value={profile}
            onChange={(event) => setProfile(event.target.value)}
            disabled={profiles.length === 0}
          >
            <option value="all">All profiles</option>
            {profiles.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex items-end gap-2">
          <span className="text-sm text-muted-foreground" role="status">
            {visible.length === series.length
              ? `${series.length} series`
              : `${visible.length} of ${series.length} series`}
          </span>
          {filterCount > 0 && (
            <Button variant="outline" size="sm" className="ml-auto" onClick={clearFilters}>
              <XIcon data-icon="inline-start" />
              Clear filters
            </Button>
          )}
        </div>
      </div>

      {visible.length === 0 ? (
        <EmptyState>No series match the current search and filters.</EmptyState>
      ) : view === 'grid' ? (
        <ul className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
          {visible.map((item) => {
            const rating = ratingText(item.metadata.rating)
            const next = nextAirdates.get(item.id)
            return (
              <li key={item.id}>
                <div
                  className={cn(
                    'group/card relative flex flex-col gap-0 overflow-hidden rounded-xl bg-card text-sm text-card-foreground shadow-xs ring-1 ring-foreground/10',
                    selected.has(item.id) && 'ring-2 ring-primary',
                  )}
                >
                  <button
                    type="button"
                    onClick={() => onOpen(item.id)}
                    className="block w-full text-left focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                    aria-label={`Open ${item.metadata.title || 'series'} details`}
                  >
                    <Poster title={item.metadata.title} poster={item.metadata.poster} className="aspect-2/3 w-full" />
                    <div className="space-y-1.5 p-3">
                      <p className="truncate text-sm font-medium" title={item.metadata.title}>
                        {item.metadata.title || 'Untitled'}
                      </p>
                      <p className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                        <span>{item.metadata.year > 0 ? item.metadata.year : 'Year unknown'}</span>
                        {rating && (
                          <span className="inline-flex items-center gap-1">
                            <StarIcon className="size-3" aria-hidden="true" />
                            {rating}
                          </span>
                        )}
                      </p>
                      <SeriesStatusBadge series={item} />
                      <Progress series={item} />
                      <p className="text-xs text-muted-foreground">
                        {next !== undefined ? `Next ${formatDate(next)}` : 'No upcoming episode'}
                      </p>
                    </div>
                  </button>
                  <span className="absolute top-2 left-2 rounded-md bg-background/85 p-1">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={selected.has(item.id)}
                      onChange={() => toggle(item.id)}
                      aria-label={`Select ${item.metadata.title || 'series'}`}
                    />
                  </span>
                </div>
              </li>
            )
          })}
        </ul>
      ) : (
        <div className="overflow-x-auto rounded-xl ring-1 ring-foreground/10">
          <table className="w-full min-w-[64rem] border-collapse text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th scope="col" className="w-10 px-3 py-2">
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={visible.length > 0 && selected.size === visible.length}
                    onChange={() =>
                      setSelectedIds((previous) =>
                        previous.size === visible.length ? new Set() : new Set(visible.map((item) => item.id)),
                      )
                    }
                    aria-label="Select all visible series"
                  />
                </th>
                <th scope="col" className="px-3 py-2 font-medium">Title</th>
                <th scope="col" className="px-3 py-2 font-medium">Status</th>
                <th scope="col" className="px-3 py-2 font-medium">Progress</th>
                <th scope="col" className="px-3 py-2 font-medium">Next air date</th>
                <th scope="col" className="px-3 py-2 font-medium">Rating</th>
                <th scope="col" className="px-3 py-2 font-medium">Monitored</th>
                <th scope="col" className="px-3 py-2 font-medium">Profile</th>
                <th scope="col" className="px-3 py-2 font-medium">Tags</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {visible.map((item) => {
                const rating = ratingText(item.metadata.rating)
                const next = nextAirdates.get(item.id)
                const profileName = profiles.find((entry) => entry.id === item.profileId)?.name || item.profileId || 'Default'
                return (
                  <tr key={item.id} className="align-top">
                    <td className="px-3 py-2.5">
                      <input
                        type="checkbox"
                        className="size-4 accent-primary"
                        checked={selected.has(item.id)}
                        onChange={() => toggle(item.id)}
                        aria-label={`Select ${item.metadata.title || 'series'}`}
                      />
                    </td>
                    <td className="px-3 py-2.5">
                      <button
                        type="button"
                        onClick={() => onOpen(item.id)}
                        className="flex items-start gap-2 rounded-sm text-left focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                      >
                        <Poster
                          title={item.metadata.title}
                          poster={item.metadata.poster}
                          className="h-12 w-8 shrink-0 rounded-sm"
                        />
                        <span className="min-w-0">
                          <span className="block font-medium">{item.metadata.title || 'Untitled'}</span>
                          <span className="block text-xs text-muted-foreground">
                            {item.metadata.year > 0 ? item.metadata.year : 'Year unknown'}
                            {item.metadata.imdbId ? ` · ${item.metadata.imdbId}` : ''}
                          </span>
                        </span>
                      </button>
                    </td>
                    <td className="px-3 py-2.5">
                      <SeriesStatusBadge series={item} />
                    </td>
                    <td className="px-3 py-2.5">
                      <Progress series={item} />
                    </td>
                    <td className="px-3 py-2.5 text-muted-foreground">
                      {next !== undefined ? formatDate(next) : 'None'}
                    </td>
                    <td className="px-3 py-2.5 text-muted-foreground">{rating ?? 'Unknown'}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{item.monitored ? 'Yes' : 'No'}</td>
                    <td className="px-3 py-2.5 text-muted-foreground">{profileName}</td>
                    <td className="px-3 py-2.5">
                      {tagsOf(item).length > 0 ? (
                        <span className="flex flex-wrap gap-1">
                          {tagsOf(item).map((tag) => (
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

      {selected.size > 0 && (
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
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
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
              aria-label="Bulk monitor mode"
              value={bulk.monitorMode}
              onChange={(event) => setBulk({ ...bulk, monitorMode: event.target.value })}
            >
              <option value="keep">Monitor mode: keep</option>
              {monitorModes.map((mode) => (
                <option key={mode.value} value={mode.value}>
                  Monitor mode: {mode.label}
                </option>
              ))}
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

function WantedTab({
  active,
  series,
  details,
  onOpenSeries,
  onGrabbed,
}: {
  active: boolean
  series: Series[]
  details: Record<string, Series>
  onOpenSeries: (id: string) => void
  onGrabbed: (id: string) => void
}) {
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<string>('')
  const [release, setRelease] = useState<{ series: Series; target: Target } | null>(null)

  const pendingDetails = series.filter((item) => seriesNeedsAttention(item) && !details[item.id])
  const rows = useMemo(() => wantedRows(series, details), [series, details])
  const searchable = rows.filter((row) => airDateValue(row.episode.airDate) !== null).length

  const runSync = async () => {
    setSyncing(true)
    setError('')
    setResult('')
    try {
      const summary = await tvApi.sync()
      setResult(`Searched ${summary.searched}, queued ${summary.queued}, imported ${summary.imported}.`)
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
          Monitored episodes with a missing file or a file below the profile cutoff appear here. Future episodes and
          active downloads stay out, and unknown air dates wait for a date before searching.
        </p>
        <Button
          size="sm"
          disabled={syncing || searchable === 0}
          title={searchable === 0 ? 'Nothing has an air date to search yet.' : undefined}
          onClick={() => void runSync()}
        >
          {syncing ? (
            <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
          ) : (
            <SearchIcon data-icon="inline-start" />
          )}
          {syncing ? 'Searching…' : 'Search missing & upgrades'}
        </Button>
      </div>

      {error && <ErrorNote onRetry={() => void runSync()}>{error}</ErrorNote>}
      {result && <Notice>{result}</Notice>}
      {pendingDetails.length > 0 && (
        <LoadingNote>
          Loading episode details for {Math.min(pendingDetails.length, detailFetchLimit)} of {pendingDetails.length}{' '}
          series that need attention…
        </LoadingNote>
      )}

      {rows.length === 0 ? (
        <EmptyState>
          Nothing is waiting. Missing monitored episodes and below-cutoff upgrades appear here.
        </EmptyState>
      ) : (
        <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
          {rows.map(({ series: item, episode }) => {
            const at = airDateValue(episode.airDate)
            const upgrade = episode.status === 'cutoff-unmet'
            const current = bestFile(episode)
            return (
              <li key={episode.id} className="flex flex-wrap items-center gap-3 p-3">
                <Poster
                  title={item.metadata.title}
                  poster={item.metadata.poster}
                  className="h-14 w-10 shrink-0 rounded-sm"
                />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">
                    {item.metadata.title || 'Untitled'}
                    <span className="font-normal text-muted-foreground">
                      {' '}
                      · {seasonLabel(episode.season)} episode {episode.number}
                    </span>
                  </p>
                  <p className="truncate text-xs text-muted-foreground">
                    {episode.title || 'Untitled episode'} ·{' '}
                    {at === null ? 'Air date unknown' : `Aired ${formatDate(at)}`}
                    {upgrade && current ? ` · Current file ${current.quality || 'quality unknown'}` : ''}
                    {episode.lastSearchAt ? ` · Last search ${formatAge(episode.lastSearchAt)}` : ''}
                  </p>
                  {upgrade && <p className="text-xs text-amber-300">Below the profile cutoff; an upgrade is wanted.</p>}
                  {at === null && !upgrade && (
                    <p className="flex items-center gap-1 text-xs text-amber-300">
                      <CircleAlertIcon className="size-3.5 shrink-0" />
                      Waiting for a known air date; automatic searches skip this episode until then.
                    </p>
                  )}
                  {episode.error && <p className="text-xs text-destructive">{episode.error}</p>}
                </div>
                {episode.status && <StatusBadge status={episode.status} />}
                <Button size="sm" variant="outline" onClick={() => onOpenSeries(item.id)}>
                  Open series
                </Button>
                <Button
                  size="sm"
                  onClick={() =>
                    setRelease({ series: item, target: { season: episode.season, episode: episode.number } })
                  }
                >
                  <SearchIcon data-icon="inline-start" />
                  Search
                </Button>
              </li>
            )
          })}
        </ul>
      )}

      {release && (
        <ReleaseDialog
          key={`${release.series.id}-${release.target.season}-${release.target.episode}`}
          active={active}
          seriesId={release.series.id}
          seriesTitle={release.series.metadata.title}
          target={release.target}
          onClose={() => setRelease(null)}
          onGrabbed={onGrabbed}
        />
      )}
    </div>
  )
}

function CalendarTab({
  entries,
  error,
  onRetry,
  onOpenSeries,
}: {
  entries: CalendarEntry[] | null
  error: string
  onRetry: () => void
  onOpenSeries: (id: string) => void
}) {
  const groups = useMemo(() => {
    const map = new Map<string, { at: number | null; entries: CalendarEntry[] }>()
    for (const entry of entries ?? []) {
      const at = airDateValue(entry.airDate)
      const key = at === null ? 'unknown' : new Date(at).toLocaleDateString(undefined, { year: 'numeric', month: 'long' })
      const group = map.get(key) ?? { at, entries: [] }
      group.entries.push(entry)
      map.set(key, group)
    }
    return [...map.entries()]
      .map(([key, group]) => ({
        key,
        at: group.at,
        entries: group.entries.sort((a, b) => (airDateValue(a.airDate) ?? 0) - (airDateValue(b.airDate) ?? 0)),
      }))
      .sort((a, b) => (a.at === null ? 1 : b.at === null ? -1 : a.at - b.at))
  }, [entries])

  if (error) return <ErrorNote onRetry={onRetry}>{error}</ErrorNote>
  if (entries === null) return <LoadingNote>Loading episode air dates…</LoadingNote>

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">
          Air dates from series metadata. Date-only air dates keep their calendar day in every timezone.
        </p>
        <Button asChild size="sm" variant="outline">
          <a href={tvApi.calendarIcsUrl()} download="constellarr-tv.ics">
            <CalendarIcon data-icon="inline-start" />
            Export .ics
          </a>
        </Button>
      </div>

      {groups.length === 0 ? (
        <EmptyState>No episodes with air dates to place on the calendar yet.</EmptyState>
      ) : (
        groups.map((group) => (
          <section key={group.key} className="space-y-2">
            <h3 className="font-heading text-sm font-semibold">
              {group.key === 'unknown' ? 'Air date unknown' : group.key}
            </h3>
            <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
              {group.entries.map((entry) => {
                const aired = airDateValue(entry.airDate) !== null && (airDateValue(entry.airDate) ?? 0) <= todayStart()
                return (
                  <li key={entry.id} className="flex flex-wrap items-center gap-3 p-3">
                    <span className="w-24 shrink-0 text-sm text-muted-foreground">
                      {formatDate(airDateValue(entry.airDate))}
                    </span>
                    <Poster title={entry.seriesTitle} poster={entry.poster} className="h-12 w-8 shrink-0 rounded-sm" />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm font-medium">{entry.seriesTitle || 'Untitled series'}</p>
                      <p className="truncate text-xs text-muted-foreground">
                        {seasonLabel(entry.season)} episode {entry.number}
                        {entry.title ? ` · ${entry.title}` : ''}
                      </p>
                    </div>
                    <Badge variant="outline">{aired ? 'Aired' : 'Upcoming'}</Badge>
                    <Button size="sm" variant="outline" onClick={() => onOpenSeries(entry.seriesId)}>
                      Open
                    </Button>
                  </li>
                )
              })}
            </ul>
          </section>
        ))
      )}
    </div>
  )
}

function ActivityTab({
  series,
  onOpenSeries,
}: {
  series: Series[]
  onOpenSeries: (id: string) => void
}) {
  const [seriesId, setSeriesId] = useState('all')
  const [entries, setEntries] = useState<TvHistoryEntry[] | null>(null)
  const [error, setError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [loadedKey, setLoadedKey] = useState('')
  const requestKey = `${seriesId}#${reloadKey}`

  useEffect(() => {
    const key = `${seriesId}#${reloadKey}`
    const controller = new AbortController()
    const request = seriesId === 'all' ? tvApi.allHistory(controller.signal) : tvApi.history(seriesId, controller.signal)
    request
      .then((result) => {
        if (controller.signal.aborted) return
        setEntries(result)
        setError('')
        setLoadedKey(key)
      })
      .catch((cause) => {
        if (controller.signal.aborted) return
        setError(errorMessage(cause))
        setLoadedKey(key)
      })
    return () => controller.abort()
  }, [seriesId, reloadKey])

  const loading = entries === null || loadedKey !== requestKey
  const titles = useMemo(() => new Map(series.map((item) => [item.id, item])), [series])
  const errored = series.filter((item) => item.error)

  return (
    <div className="flex flex-col gap-4">
      {errored.length > 0 && (
        <Section title="Current errors">
          <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
            {errored.map((item) => (
              <li key={item.id} className="flex flex-wrap items-center gap-3 p-3">
                <SeriesStatusBadge series={item} />
                <span className="min-w-0 flex-1 text-sm break-words">
                  <span className="font-medium">{item.metadata.title || 'Untitled'}</span> · {item.error}
                </span>
                <Button size="sm" variant="outline" onClick={() => onOpenSeries(item.id)}>
                  Open series
                </Button>
              </li>
            ))}
          </ul>
        </Section>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <p className="text-sm text-muted-foreground">
          {loading || entries === null ? 'Loading history…' : `${entries.length} events`}
        </p>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Select aria-label="Filter history by series" value={seriesId} onChange={(event) => setSeriesId(event.target.value)}>
            <option value="all">All series</option>
            {series.map((item) => (
              <option key={item.id} value={item.id}>
                {item.metadata.title || item.id}
              </option>
            ))}
          </Select>
          <Button size="sm" variant="outline" onClick={() => setReloadKey((current) => current + 1)}>
            <RefreshCwIcon data-icon="inline-start" />
            Refresh
          </Button>
        </div>
      </div>

      {error && (
        <ErrorNote onRetry={() => setReloadKey((current) => current + 1)}>{error}</ErrorNote>
      )}
      {loading && entries === null && !error && <LoadingNote>Loading history…</LoadingNote>}
      {!loading && entries !== null && entries.length === 0 && (
        <EmptyState>No history yet. Acquisitions, imports, and refreshes appear here.</EmptyState>
      )}
      {entries !== null && entries.length > 0 && (
        <ul className="flex flex-col divide-y divide-border rounded-xl ring-1 ring-foreground/10">
          {entries.map((entry) => {
            const item = titles.get(entry.seriesId)
            return (
              <li key={entry.id} className="flex flex-wrap items-start gap-3 p-3">
                <Badge variant="outline" className="capitalize">
                  {entry.type || 'event'}
                </Badge>
                <div className="min-w-0 flex-1">
                  <p className="text-sm break-words">{entry.message}</p>
                  <p className="text-xs text-muted-foreground">
                    {item?.metadata.title ?? entry.seriesId ?? 'Unknown series'} ·{' '}
                    <time dateTime={entry.createdAt} title={formatDateTime(entry.createdAt)}>
                      {formatAge(entry.createdAt)}
                    </time>
                  </p>
                </div>
                {item && <SeriesStatusBadge series={item} />}
                {item && (
                  <Button size="sm" variant="ghost" onClick={() => onOpenSeries(item.id)}>
                    Open
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

function ProfilesTab({ profiles, onChanged }: { profiles: MovieProfile[]; onChanged: () => void }) {
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        TV uses the same quality profiles as movies, including the cutoff and release scoring rules. Changes here
        apply to both libraries.
      </p>
      <QualityProfilesTab profiles={profiles} onChanged={onChanged} />
    </div>
  )
}

export function TVPage({ active }: { active: boolean }) {
  const [series, setSeries] = useState<Series[] | null>(null)
  const [profiles, setProfiles] = useState<MovieProfile[]>([])
  const [config, setConfig] = useState<TvConfig | null>(null)
  const [calendar, setCalendar] = useState<CalendarEntry[] | null>(null)
  const [calendarError, setCalendarError] = useState('')
  const [details, setDetails] = useState<Record<string, Series>>({})
  const [loadError, setLoadError] = useState('')
  const [setupError, setSetupError] = useState('')
  const [tab, setTab] = useState<Tab>('library')
  const [detailId, setDetailId] = useState<string | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [scanOpen, setScanOpen] = useState(false)
  const [watched, setWatched] = useState<string[]>([])
  const controller = useRef<AbortController | null>(null)
  const requestId = useRef(0)
  const detailIdRef = useRef<string | null>(null)
  const wantedIdsRef = useRef<string[]>([])
  const watchTimers = useRef<number[]>([])

  const loadDetails = useCallback(async (ids: string[], signal?: AbortSignal) => {
    const unique = [...new Set(ids)].filter(Boolean).slice(0, detailFetchLimit)
    if (unique.length === 0) return
    const results = await Promise.allSettled(unique.map((id) => tvApi.detail(id, signal)))
    if (signal?.aborted) return
    setDetails((previous) => {
      const next = { ...previous }
      results.forEach((result) => {
        if (result.status !== 'fulfilled') return
        const fetched = result.value
        const current = next[fetched.id]
        next[fetched.id] = { ...current, ...fetched, episodes: fetched.episodes ?? current?.episodes }
      })
      return next
    })
  }, [])

  const reload = useCallback(async () => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    const id = requestId.current + 1
    requestId.current = id

    const [listResult, calendarResult, profilesResult, configResult] = await Promise.allSettled([
      tvApi.list(request.signal),
      tvApi.calendar(request.signal),
      moviesApi.profiles(request.signal),
      tvApi.config(request.signal),
    ])
    if (requestId.current !== id) return

    if (listResult.status === 'fulfilled') {
      setSeries(listResult.value)
      setLoadError('')
    } else {
      setLoadError(errorMessage(listResult.reason))
    }
    if (calendarResult.status === 'fulfilled') {
      setCalendar(calendarResult.value)
      setCalendarError('')
    } else {
      setCalendarError(errorMessage(calendarResult.reason))
    }
    if (profilesResult.status === 'fulfilled') setProfiles(profilesResult.value)
    if (configResult.status === 'fulfilled') setConfig(configResult.value)
    const failedSettings = [profilesResult, configResult].find((result) => result.status === 'rejected')
    setSetupError(failedSettings ? errorMessage(failedSettings.reason) : '')

    const ids = [detailIdRef.current, ...wantedIdsRef.current].filter((value): value is string => Boolean(value))
    if (ids.length > 0) await loadDetails(ids, request.signal)
  }, [loadDetails])

  const live = useMemo(() => {
    const listActive = (series ?? []).some((item) => activeStatuses.has(stateKey(item)))
    const detailActive = Object.values(details).some((item) =>
      (item.episodes ?? []).some((episode) => activeStatuses.has(episode.status)),
    )
    return listActive || detailActive
  }, [series, details])
  const pollMs = live || watched.length > 0 ? 5000 : 60000

  // The page stays mounted while hidden; poll only on the TV route and pause when the tab is backgrounded.
  useEffect(() => {
    if (!active) return
    let wake: number | undefined
    const tick = () => {
      if (document.visibilityState === 'visible') void reload()
    }
    tick()
    const timer = window.setInterval(tick, pollMs)
    const onWake = () => {
      window.clearTimeout(wake)
      wake = window.setTimeout(tick, 0)
    }
    document.addEventListener('visibilitychange', onWake)
    return () => {
      window.clearTimeout(wake)
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onWake)
      controller.current?.abort()
    }
  }, [active, pollMs, reload])

  useEffect(() => {
    const refresh = () => void reload()
    window.addEventListener('tv-changed', refresh)
    return () => window.removeEventListener('tv-changed', refresh)
  }, [reload])

  useEffect(
    () => () => {
      watchTimers.current.forEach((timer) => window.clearTimeout(timer))
      watchTimers.current = []
    },
    [],
  )

  // Episode details are fetched for series that can contribute missing or upgrade rows, not the whole library.
  useEffect(() => {
    if (!active || tab !== 'wanted' || series === null) return
    const ids = series.filter(seriesNeedsAttention).map((item) => item.id)
    wantedIdsRef.current = ids
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetches settle
    void loadDetails(ids)
  }, [active, tab, series, loadDetails])

  // Stop background detail polls once the wanted tab is no longer visible.
  useEffect(() => {
    if (tab !== 'wanted') wantedIdsRef.current = []
  }, [tab])

  const watchSeries = useCallback((id: string) => {
    setWatched((previous) => (previous.includes(id) ? previous : [...previous, id]))
    watchTimers.current.push(
      window.setTimeout(() => setWatched((previous) => previous.filter((item) => item !== id)), 5 * 60_000),
    )
  }, [])

  const openSeries = useCallback(
    (id: string) => {
      setDetailId(id)
      detailIdRef.current = id
      void loadDetails([id])
    },
    [loadDetails],
  )

  const addSeries = useCallback(async (input: AddSeriesInput) => {
    const created = await tvApi.add(input)
    setSeries((list) => (list ? [created, ...list.filter((item) => item.id !== created.id)] : [created]))
    return created
  }, [])

  const saveSeries = useCallback(async (updated: Series) => {
    const saved = await tvApi.update(updated.id, updated)
    setSeries(
      (list) =>
        list?.map((item) =>
          item.id === saved.id ? { ...item, ...saved, episodes: saved.episodes ?? item.episodes } : item,
        ) ?? list,
    )
    setDetails((previous) => {
      const current = previous[saved.id]
      return { ...previous, [saved.id]: { ...current, ...saved, episodes: saved.episodes ?? current?.episodes } }
    })
    return saved
  }, [])

  const removeSeries = useCallback(async (id: string, deleteFiles: boolean) => {
    await tvApi.remove(id, deleteFiles)
    setSeries((list) => list?.filter((item) => item.id !== id) ?? list)
    setDetails((previous) => {
      const next = { ...previous }
      delete next[id]
      return next
    })
  }, [])

  const bulkEdit = useCallback(async (input: BulkSeriesInput) => {
    const updated = await tvApi.bulk(input)
    const byId = new Map(updated.map((item) => [item.id, item]))
    setSeries((list) => list?.map((item) => byId.get(item.id) ?? item) ?? list)
  }, [])

  // The import response is not decorated with counts, so refresh the list instead of merging it.
  const mergeImported = useCallback(
    (imported: Series) => {
      void loadDetails([imported.id])
      void reload()
    },
    [loadDetails, reload],
  )

  const nextAirdates = useMemo(() => {
    const map = new Map<string, number>()
    const today = todayStart()
    for (const entry of calendar ?? []) {
      const at = airDateValue(entry.airDate)
      if (at === null || at < today) continue
      const current = map.get(entry.seriesId)
      if (current === undefined || at < current) map.set(entry.seriesId, at)
    }
    return map
  }, [calendar])

  const roots = config?.rootFolders ?? []
  const wantedCount = (series ?? []).reduce((count, item) => count + (item.monitored ? item.wanted : 0), 0)
  const detailSeries = detailId
    ? (details[detailId] ?? (series ?? []).find((item) => item.id === detailId) ?? null)
    : null

  const tabs: { id: Tab; label: string; badge?: number }[] = [
    { id: 'library', label: 'Library' },
    { id: 'wanted', label: 'Wanted', badge: wantedCount },
    { id: 'calendar', label: 'Calendar' },
    { id: 'activity', label: 'Activity' },
    { id: 'profiles', label: 'Profiles', badge: profiles.length },
  ]

  const onTabKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft') return
    event.preventDefault()
    const index = tabs.findIndex((item) => item.id === tab)
    const offset = event.key === 'ArrowRight' ? 1 : -1
    const next = tabs[(index + offset + tabs.length) % tabs.length].id
    setTab(next)
    document.getElementById(`tv-tab-${next}`)?.focus()
  }

  let panel: ReactNode
  if (series === null) {
    panel = loadError ? (
      <ErrorNote onRetry={() => void reload()}>Could not load the TV library. {loadError}</ErrorNote>
    ) : (
      <LoadingNote>Loading TV library…</LoadingNote>
    )
  } else {
    panel = (
      <div role="tabpanel" id="tv-panel" aria-labelledby={`tv-tab-${tab}`}>
        <div hidden={tab !== 'library'}>
          <LibraryView
            series={series}
            profiles={profiles}
            roots={roots}
            nextAirdates={nextAirdates}
            onOpen={openSeries}
            onAdd={() => setAddOpen(true)}
            onScan={() => setScanOpen(true)}
            onBulk={bulkEdit}
          />
        </div>
        {tab === 'wanted' && (
          <WantedTab active={active} series={series} details={details} onOpenSeries={openSeries} onGrabbed={watchSeries} />
        )}
        {tab === 'calendar' && (
          <CalendarTab
            entries={calendar}
            error={calendarError}
            onRetry={() => void reload()}
            onOpenSeries={openSeries}
          />
        )}
        {tab === 'activity' && <ActivityTab series={series} onOpenSeries={openSeries} />}
        {tab === 'profiles' && <ProfilesTab profiles={profiles} onChanged={() => void reload()} />}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeading
        title="TV Shows"
        description="Search, organize, and monitor your series, seasons, and episodes."
        action={
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => setScanOpen(true)}>
              <FolderSearchIcon data-icon="inline-start" />
              Scan library
            </Button>
            <Button size="sm" onClick={() => setAddOpen(true)}>
              <PlusIcon data-icon="inline-start" />
              Add series
            </Button>
          </div>
        }
      />

      {loadError && series !== null && (
        <ErrorNote onRetry={() => void reload()}>Could not refresh the TV library. {loadError}</ErrorNote>
      )}
      {setupError && (
        <ErrorNote onRetry={() => void reload()}>Some TV settings could not be loaded. {setupError}</ErrorNote>
      )}

      {series !== null && (
        <div
          role="tablist"
          aria-label="TV sections"
          onKeyDown={onTabKeyDown}
          className="flex flex-wrap gap-1 border-b border-border"
        >
          {tabs.map((item) => (
            <button
              key={item.id}
              type="button"
              role="tab"
              id={`tv-tab-${item.id}`}
              aria-selected={tab === item.id}
              aria-controls="tv-panel"
              tabIndex={tab === item.id ? 0 : -1}
              onClick={() => setTab(item.id)}
              className={cn(
                '-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
                tab === item.id
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
      )}

      {panel}

      {addOpen && (
        <AddSeriesDialog
          active={active}
          profiles={profiles}
          roots={roots}
          onClose={() => setAddOpen(false)}
          onAdded={addSeries}
        />
      )}

      {scanOpen && (
        <ScanDialog
          active={active}
          series={series ?? []}
          roots={roots}
          importMode={config?.importMode ?? ''}
          onClose={() => setScanOpen(false)}
          onImported={mergeImported}
        />
      )}

      {detailSeries && (
        <SeriesDetailDialog
          key={detailSeries.id}
          active={active}
          series={detailSeries}
          profiles={profiles}
          roots={roots}
          onClose={() => {
            setDetailId(null)
            detailIdRef.current = null
          }}
          onSave={saveSeries}
          onReload={() => void loadDetails([detailSeries.id])}
          onRemove={removeSeries}
          onGrabbed={watchSeries}
          onChanged={() => void reload()}
        />
      )}
    </div>
  )
}
