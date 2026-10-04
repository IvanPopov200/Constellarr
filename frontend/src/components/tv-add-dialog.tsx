import { useEffect, useRef, useState } from 'react'
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  CircleAlertIcon,
  LoaderCircleIcon,
  PlusIcon,
  SearchIcon,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox, DialogShell, EmptyState, ErrorNote, Notice, Poster, Select } from '@/components/tv-ui'
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
  onClose,
  onAdded,
}: {
  active: boolean
  profiles: MovieProfile[]
  roots: RootFolder[]
  onClose: () => void
  onAdded: (input: AddSeriesInput) => Promise<Series>
}) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SeriesTitle[] | null>(null)
  const [page, setPage] = useState(1)
  const [busy, setBusy] = useState<'search' | 'add' | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [imdbId, setImdbId] = useState('')
  const [options, setOptions] = useState({
    monitored: true,
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

  useEffect(() => () => controller.current?.abort(), [])

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
    setNotice('')
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

  const addResult = async (candidate: SeriesTitle) => {
    setBusy('add')
    setError('')
    setNotice('')
    try {
      const series = await onAdded(
        candidate.imdbId ? { imdbId: candidate.imdbId, ...base } : { metadata: candidate, ...base },
      )
      setNotice(`Added ${series.metadata.title || candidate.title}.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const addByImdb = async () => {
    const id = imdbId.trim()
    if (!id) {
      setError('Enter an IMDb ID such as tt0944947.')
      return
    }
    setBusy('add')
    setError('')
    setNotice('')
    try {
      const series = await onAdded({ imdbId: id, ...base })
      setNotice(`Added ${series.metadata.title || id}.`)
      setImdbId('')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const addManual = async () => {
    if (!fields.title.trim()) {
      setError('Enter at least a title for an offline series.')
      return
    }
    setBusy('add')
    setError('')
    setNotice('')
    try {
      const series = await onAdded({ metadata: manualSeries(fields), ...base })
      setNotice(`Added ${series.metadata.title || fields.title}.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
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

  return (
    <DialogShell
      active={active}
      title="Add series"
      description="Search series metadata, add by IMDb ID, or enter an offline series manually."
      onClose={onClose}
    >
      <div className="space-y-5">
        <div className="grid gap-4 rounded-lg border border-border p-3 sm:grid-cols-2 xl:grid-cols-3">
          <Checkbox
            id="tv-add-monitored"
            label="Monitored"
            checked={options.monitored}
            onChange={(monitored) => setOptions({ ...options, monitored })}
          />
          <div className="space-y-2">
            <label htmlFor="tv-add-mode" className="text-sm font-medium">
              Monitor mode
            </label>
            <Select
              id="tv-add-mode"
              className="w-full"
              value={options.monitorMode}
              disabled={!options.monitored}
              onChange={(event) => setOptions({ ...options, monitorMode: event.target.value })}
            >
              {monitorModes.map((mode) => (
                <option key={mode.value} value={mode.value}>
                  {mode.label}
                </option>
              ))}
            </Select>
          </div>
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
          {roots.length === 0 && (
            <p className="flex items-center gap-2 text-xs text-amber-300 sm:col-span-2">
              <CircleAlertIcon className="size-4 shrink-0" />
              No TV root folder configured. Add one in{' '}
              <a href="#storage" className="underline underline-offset-4">
                Storage & Paths
              </a>{' '}
              so imports have a destination.
            </p>
          )}
        </div>

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
          {results === null ? (
            <p className="text-sm text-muted-foreground">
              Results appear here with poster, year, rating, and season count.
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
                      <Button size="sm" disabled={busy !== null} onClick={() => void addResult(candidate)}>
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
        </section>

        <section className="space-y-3 border-t border-border pt-4">
          <h3 className="font-heading text-sm font-semibold">Add by IMDb ID</h3>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              value={imdbId}
              onChange={(event) => setImdbId(event.target.value)}
              placeholder="tt0944947"
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
          <h3 className="font-heading text-sm font-semibold">Offline series</h3>
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
            <Button size="sm" disabled={busy !== null} onClick={() => void addManual()}>
              <PlusIcon data-icon="inline-start" />
              Add offline series
            </Button>
          </div>
        </section>

        {error && <ErrorNote>{error}</ErrorNote>}
        {notice && <Notice>{notice}</Notice>}
      </div>
    </DialogShell>
  )
}
