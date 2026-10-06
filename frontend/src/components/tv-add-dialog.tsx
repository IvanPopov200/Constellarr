import { useEffect, useRef, useState } from 'react'
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  CircleAlertIcon,
  LoaderCircleIcon,
  PlusIcon,
  SearchIcon,
  Settings2Icon,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox, DialogShell, EmptyState, ErrorNote, Poster, Select } from '@/components/tv-ui'
import { errorMessage } from '@/lib/api'
import { monitorModes, splitList, strings } from '@/components/tv-shared'
import { tvApi, type AddSeriesInput, type MovieProfile, type RootFolder, type Series, type SeriesTitle } from '@/lib/tv-api'

type ManualFields = {
  title: string
  year: string
  imdbId: string
  totalSeasons: string
  poster: string
}

function manualSeries(fields: ManualFields): SeriesTitle {
  return {
    imdbId: fields.imdbId.trim(),
    title: fields.title.trim(),
    year: Number(fields.year) || 0,
    type: 'series',
    totalSeasons: Number(fields.totalSeasons) || 0,
    released: '',
    rating: null,
    votes: 0,
    runtime: 0,
    directors: [],
    cast: [],
    genres: [],
    languages: [],
    countries: [],
    certification: '',
    poster: fields.poster.trim(),
    plot: '',
  }
}

export function AddSeriesDialog({
  active,
  profiles,
  roots,
  knownSeriesIds,
  canReadSettings,
  onClose,
  onAdded,
  onOpenSeries,
  onSearchReleases,
}: {
  active: boolean
  profiles: MovieProfile[]
  roots: RootFolder[]
  knownSeriesIds: string[]
  canReadSettings: boolean
  onClose: () => void
  onAdded: (input: AddSeriesInput) => Promise<Series>
  onOpenSeries: (id: string) => void
  onSearchReleases: (series: Series) => void
}) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SeriesTitle[] | null>(null)
  const [page, setPage] = useState(1)
  const [busy, setBusy] = useState<'search' | 'add' | null>(null)
  const [error, setError] = useState('')
  const [added, setAdded] = useState<Series | null>(null)
  const [alreadyAdded, setAlreadyAdded] = useState(false)
  const [advanced, setAdvanced] = useState(false)
  const [imdbId, setImdbId] = useState('')
  const [options, setOptions] = useState({
    monitored: false,
    monitorMode: 'all',
    profileId: '',
    rootId: '',
    tags: '',
  })
  const [fields, setFields] = useState<ManualFields>({
    title: '',
    year: '',
    imdbId: '',
    totalSeasons: '',
    poster: '',
  })
  const controller = useRef<AbortController | null>(null)
  const confirmation = useRef<HTMLHeadingElement>(null)
  // Catalog contents when the dialog opened, so an existing series is reported instead of a fresh add.
  const [knownIds] = useState(() => new Set(knownSeriesIds))

  useEffect(() => () => controller.current?.abort(), [])

  useEffect(() => {
    if (added) confirmation.current?.focus()
  }, [added])

  const base = {
    monitored: options.monitored,
    monitorMode: options.monitorMode,
    profileId: options.profileId || profiles[0]?.id || '',
    rootId: options.rootId || roots[0]?.id || '',
    tags: splitList(options.tags),
  }

  const search = async (nextPage: number) => {
    const term = query.trim()
    if (!term) return
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    setBusy('search')
    setError('')
    try {
      const found = await tvApi.discover(term, nextPage, request.signal)
      if (request.signal.aborted) return
      setResults(found)
      setPage(nextPage)
    } catch (cause) {
      if (!request.signal.aborted) setError(errorMessage(cause))
    } finally {
      if (!request.signal.aborted) setBusy(null)
    }
  }

  const add = async (input: AddSeriesInput) => {
    if (busy !== null || added) return
    setBusy('add')
    setError('')
    try {
      const series = await onAdded(input)
      setAlreadyAdded(knownIds.has(series.id))
      setAdded(series)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const addResult = (candidate: SeriesTitle) =>
    add(candidate.imdbId ? { imdbId: candidate.imdbId, ...base } : { metadata: candidate, ...base })

  const addByImdb = () => {
    const id = imdbId.trim()
    if (!id) {
      setError('Enter an IMDb ID such as tt0944947.')
      return
    }
    return add({ imdbId: id, ...base })
  }

  const addManual = () => {
    if (!fields.title.trim()) {
      setError('Enter at least a title for an offline series.')
      return
    }
    return add({ metadata: manualSeries(fields), ...base })
  }

  const manualField = (key: keyof ManualFields, label: string, props: { type?: string } = {}) => (
    <div className="space-y-2">
      <label htmlFor={`tv-manual-${key}`} className="text-sm font-medium">
        {label}
      </label>
      <Input
        id={`tv-manual-${key}`}
        type={props.type}
        value={fields[key]}
        onChange={(event) => setFields({ ...fields, [key]: event.target.value })}
      />
    </div>
  )

  if (added) {
    const title = added.metadata.title || 'The series'
    const offline = !added.metadata.imdbId
    return (
      <DialogShell
        active={active}
        title={alreadyAdded ? 'Already in your library' : 'Series added'}
        description={`${added.metadata.year > 0 ? added.metadata.year : 'Year unknown'} · ${
          alreadyAdded ? 'no new entry was created' : 'added to your TV library'
        }`}
        onClose={onClose}
      >
        <div className="space-y-4 rounded-lg border border-border p-4">
          <h3
            id="tv-add-confirmation-title"
            ref={confirmation}
            tabIndex={-1}
            className="font-heading text-sm font-semibold focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {alreadyAdded ? `${title} is already in your library.` : `Added ${title}.`}
          </h3>
          <p role="status" className="text-sm text-muted-foreground">
            {alreadyAdded
              ? 'Nothing changed. Opening it shows its seasons and episodes, where you can pick a release yourself.'
              : added.monitored
                ? 'Automatic downloads are on for this series: missing aired episodes appear in Wanted and Constellarr searches indexers for them. Nothing is downloaded until a release matches.'
                : 'Automatic downloads are off: nothing is searched or downloaded. The series waits in Wanted until you choose a release, and you can turn automatic downloads on later in its settings.'}
          </p>
          {!alreadyAdded && offline && (
            <p className="text-sm text-muted-foreground">
              This is an offline series, so no episodes came from a metadata provider. Open it to add seasons and
              episodes by hand, then search for releases.
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            {(alreadyAdded || offline) && (
              <Button size="sm" onClick={() => onOpenSeries(added.id)}>
                Open series
              </Button>
            )}
            <Button
              size="sm"
              variant={alreadyAdded || offline ? 'outline' : 'default'}
              onClick={() => onSearchReleases(added)}
            >
              <SearchIcon data-icon="inline-start" />
              Search releases
            </Button>
            <Button size="sm" variant="ghost" onClick={onClose}>
              Done
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            Searching releases only lists candidates; nothing downloads until you confirm one.
          </p>
        </div>
      </DialogShell>
    )
  }

  return (
    <DialogShell
      active={active}
      title="Add series"
      description="Add a series, then choose the episodes and releases to download."
      onClose={onClose}
    >
      <div className="space-y-5">
        <section className="space-y-3">
          <h3 className="font-heading text-sm font-semibold">Search series metadata</h3>
          <form
            className="flex flex-col gap-2 sm:flex-row"
            onSubmit={(event) => {
              event.preventDefault()
              void search(1)
            }}
          >
            <Input
              autoFocus
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Series title"
              aria-label="Search series metadata"
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

          <div className="space-y-3 rounded-lg border border-border p-3">
            <Checkbox
              id="tv-add-monitored"
              label="Download automatically"
              description={
                options.monitored
                  ? 'Constellarr searches indexers and downloads releases for this series later without asking again.'
                  : undefined
              }
              checked={options.monitored}
              onChange={(monitored) => setOptions({ ...options, monitored })}
            />
            {options.monitored && <div className="space-y-2 sm:max-w-xs">
              <label htmlFor="tv-add-mode" className="text-sm font-medium">
                Episodes to download
              </label>
              <Select
                id="tv-add-mode"
                className="w-full"
                value={options.monitorMode}
                onChange={(event) => setOptions({ ...options, monitorMode: event.target.value })}
              >
                {monitorModes.map((mode) => (
                  <option key={mode.value} value={mode.value}>
                    {mode.label}
                  </option>
                ))}
              </Select>
            </div>}
          </div>
          {results === null ? (
            <p className="text-sm text-muted-foreground">
              Search by title to find a series.
            </p>
          ) : results.length === 0 ? (
            <EmptyState>No series metadata on this page. Try another title or the next page.</EmptyState>
          ) : (
            <>
              <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
                {results.map((candidate) => {
                  const genres = strings(candidate.genres)
                  const rating = typeof candidate.rating === 'number' && candidate.rating > 0 ? candidate.rating.toFixed(1) : null
                  return (
                    <li
                      key={`${candidate.imdbId}-${candidate.title}-${candidate.year}`}
                      className="flex flex-wrap items-center gap-3 p-3"
                    >
                      <Poster
                        title={candidate.title}
                        poster={candidate.poster}
                        className="h-16 w-11 shrink-0 rounded-sm"
                      />
                      <div className="min-w-0 flex-1">
                        <p className="text-sm font-medium">
                          {candidate.title || 'Untitled'}{' '}
                          <span className="font-normal text-muted-foreground">
                            {candidate.year > 0 ? candidate.year : 'Year unknown'}
                          </span>
                        </p>
                        <p className="text-xs text-muted-foreground">
                          {candidate.imdbId || 'No IMDb ID'}
                          {rating ? ` · ${rating}/10` : ''}
                          {candidate.totalSeasons && candidate.totalSeasons > 0
                            ? ` · ${candidate.totalSeasons} season${candidate.totalSeasons === 1 ? '' : 's'}`
                            : ''}
                          {genres.length > 0 ? ` · ${genres.slice(0, 2).join(', ')}` : ''}
                        </p>
                      </div>
                      <Button
                        size="sm"
                        disabled={busy !== null}
                        aria-label={`Add ${candidate.title || 'series'}`}
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
                  )
                })}
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
          <p className="text-xs text-muted-foreground">
            {options.monitored
              ? 'Adding starts automatic downloads for the selected episodes.'
              : 'Added series stay unsearched until you choose a release or turn on automatic downloads.'}
          </p>
        </section>

        {error && <ErrorNote>{error}</ErrorNote>}

        <section className="space-y-3 border-t border-border pt-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Button
              size="sm"
              variant="ghost"
              aria-expanded={advanced}
              aria-controls="tv-add-advanced"
              onClick={() => setAdvanced((current) => !current)}
            >
              <Settings2Icon data-icon="inline-start" />
              {advanced ? 'Hide advanced options' : 'Advanced options'}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {canReadSettings
              ? "New series use the server's default quality profile and root folder unless you change them."
              : "Series use the server's default TV root folder and quality profile."}
          </p>
          {canReadSettings && roots.length === 0 && (
            <p className="flex items-center gap-2 text-xs text-amber-300">
              <CircleAlertIcon className="size-4 shrink-0" />
              No TV root folder configured. Add one in{' '}
              <a href="#storage" className="underline underline-offset-4">
                Storage & Paths
              </a>{' '}
              so imports have a destination.
            </p>
          )}

          {advanced && (
            <div id="tv-add-advanced" className="space-y-5">
              <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
                {canReadSettings && (
                  <div className="space-y-2">
                    <label htmlFor="tv-add-profile" className="text-sm font-medium">
                      Quality profile
                    </label>
                    <Select
                      id="tv-add-profile"
                      className="w-full"
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
                    <label htmlFor="tv-add-root" className="text-sm font-medium">
                      Root folder
                    </label>
                    <Select
                      id="tv-add-root"
                      className="w-full"
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
                  <label htmlFor="tv-add-tags" className="text-sm font-medium">
                    Tags
                  </label>
                  <Input
                    id="tv-add-tags"
                    value={options.tags}
                    placeholder="kids, anime"
                    onChange={(event) => setOptions({ ...options, tags: event.target.value })}
                  />
                </div>
              </div>

              <div className="space-y-3">
                <h4 className="font-heading text-sm font-semibold">Add by IMDb ID</h4>
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Input
                    value={imdbId}
                    onChange={(event) => setImdbId(event.target.value)}
                    placeholder="tt0944947"
                    aria-label="IMDb ID"
                    className="sm:max-w-xs"
                  />
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy !== null || !imdbId.trim()}
                    onClick={() => void addByImdb()}
                  >
                    <PlusIcon data-icon="inline-start" />
                    Add by IMDb ID
                  </Button>
                </div>
              </div>

              <div className="space-y-3">
                <h4 className="font-heading text-sm font-semibold">Offline series</h4>
                <p className="text-xs text-muted-foreground">
                  Use this when there is no metadata provider or the series is not listed. Episodes are matched
                  manually after adding.
                </p>
                <div className="grid gap-4 sm:grid-cols-2">
                  {manualField('title', 'Title')}
                  {manualField('year', 'Year', { type: 'number' })}
                  {manualField('imdbId', 'IMDb ID (optional)')}
                  {manualField('totalSeasons', 'Seasons (optional)', { type: 'number' })}
                  <div className="sm:col-span-2">{manualField('poster', 'Poster URL (optional)', { type: 'url' })}</div>
                </div>
                <div className="flex justify-end">
                  <Button size="sm" disabled={busy !== null || !fields.title.trim()} onClick={() => void addManual()}>
                    <PlusIcon data-icon="inline-start" />
                    Add offline series
                  </Button>
                </div>
                <p className="text-xs text-muted-foreground">A title is required; every other field is optional.</p>
              </div>
            </div>
          )}
        </section>
      </div>
    </DialogShell>
  )
}
