import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ActivityIcon,
  CircleAlertIcon,
  FilmIcon,
  LanguagesIcon,
  LoaderCircleIcon,
  RefreshCwIcon,
  ScanSearchIcon,
  SettingsIcon,
  TvIcon,
  Wand2Icon,
} from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { useAuth } from '@/lib/auth-context'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SubtitleDetailDialog } from '@/components/subtitles-detail'
import { SubtitleSettings } from '@/components/subtitles-settings'
import {
  ActionNote,
  Disclosure,
  EmptyNote,
  ErrorNote,
  Select,
  TabButtons,
  VariantBadges,
  historyActionLabel,
  jobKindLabel,
  languageName,
  sourceLabel,
  variantText,
  videoMeta,
  videoTitle,
} from '@/components/subtitles-ui'
import { errorMessage } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'
import {
  statusVariant,
  subtitlesApi,
  timeLabel,
  type SubtitleHistoryEntry,
  type SubtitleJob,
  type SubtitleLibraryItem,
  type SubtitleProfile,
  type SubtitleProviderStatus,
  type SubtitleWantedItem,
} from '@/lib/subtitles-api'

type Tab = 'library' | 'missing' | 'activity' | 'settings'

type Target = { kind: string; id: string; mode: 'detail' | 'search' }

const tabs: { value: Tab; label: string; icon: typeof LanguagesIcon }[] = [
  { value: 'library', label: 'Library', icon: LanguagesIcon },
  { value: 'missing', label: 'Missing', icon: CircleAlertIcon },
  { value: 'activity', label: 'Activity', icon: ActivityIcon },
  { value: 'settings', label: 'Settings', icon: SettingsIcon },
]

const activeJobStatuses = new Set(['queued', 'running'])

function isActive(job: SubtitleJob) {
  return activeJobStatuses.has(job.status)
}

function LibraryCard({
  item,
  canWrite,
  profileName,
  writeReasonId,
  onOpen,
}: {
  item: SubtitleLibraryItem
  canWrite: boolean
  profileName: string
  writeReasonId: string
  onOpen: (mode: 'detail' | 'search') => void
}) {
  const video = item.video
  const missing = item.wanted.filter((row) => row.status === 'wanted')
  return (
    <li className="flex flex-col gap-3 rounded-lg border border-border bg-card p-3">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate font-medium">{videoTitle(video)}</p>
          <p className="text-xs text-muted-foreground">{videoMeta(video)}</p>
        </div>
        <Badge variant={item.missing > 0 ? 'destructive' : 'secondary'}>
          {item.missing > 0 ? `${item.missing} missing` : 'complete'}
        </Badge>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        {item.sidecars.length === 0 ? (
          <span className="text-xs text-muted-foreground">No subtitle files yet</span>
        ) : (
          item.sidecars.map((sidecar) => <VariantBadges key={sidecar.path} item={sidecar} />)
        )}
        {item.outputs.length > 0 && <Badge variant="outline">{`${item.outputs.length} waiting for review`}</Badge>}
      </div>
      {missing.length > 0 && <p className="text-xs text-muted-foreground">Looking for {missing.map(variantText).join(', ')}</p>}
      {item.monitored ? (
        profileName && <p className="text-xs text-muted-foreground">Language profile: {profileName}</p>
      ) : (
        <p className="text-xs text-amber-500">Not monitored, so subtitles are not searched automatically.</p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          onClick={() => onOpen('search')}
          disabled={!canWrite}
          aria-describedby={canWrite ? undefined : writeReasonId}
          title={canWrite ? undefined : 'Requires the subtitles write permission'}
        >
          <ScanSearchIcon data-icon="inline-start" />
          Find subtitles
        </Button>
        <Button size="sm" variant="ghost" onClick={() => onOpen('detail')}>
          <Wand2Icon data-icon="inline-start" />
          {canWrite ? 'Manage subtitles' : 'View subtitles'}
        </Button>
      </div>
      <Disclosure variant="inline" label="File details">
        <dl className="space-y-1">
          <div>
            <dt className="inline">Video file: </dt>
            <dd className="inline break-all">{video.path}</dd>
          </div>
          <div>
            <dt className="inline">Monitored: </dt>
            <dd className="inline">{item.monitored ? 'yes' : 'no'}</dd>
          </div>
          {item.profileId && (
            <div>
              <dt className="inline">Profile id: </dt>
              <dd className="inline">{item.profileId}</dd>
            </div>
          )}
          {video.imdbId && (
            <div>
              <dt className="inline">IMDb: </dt>
              <dd className="inline">{video.imdbId}</dd>
            </div>
          )}
          {video.seriesImdbId && (
            <div>
              <dt className="inline">Series IMDb: </dt>
              <dd className="inline">{video.seriesImdbId}</dd>
            </div>
          )}
          {item.sidecars.length > 0 && (
            <div>
              <dt>Subtitle files:</dt>
              <dd>
                <ul className="space-y-1">
                  {item.sidecars.map((sidecar) => (
                    <li key={sidecar.path} className="break-all">
                      {sidecar.path} · {sidecar.format.toUpperCase()} · {formatBytes(sidecar.size)} · {sourceLabel(sidecar.source)} ·{' '}
                      {timeLabel(sidecar.updatedAt)}
                    </li>
                  ))}
                </ul>
              </dd>
            </div>
          )}
        </dl>
      </Disclosure>
    </li>
  )
}

export function SubtitlesPage({ target }: { target?: { kind: string; id: string; mode?: 'detail' | 'search' } } = {}) {
  const { can } = useAuth()
  const canRead = can('subtitles.read')
  const canWrite = can('subtitles.write')
  const canSettingsRead = can('settings.read')
  const [tab, setTab] = useState<Tab>('library')
  const [items, setItems] = useState<SubtitleLibraryItem[]>([])
  const [wanted, setWanted] = useState<SubtitleWantedItem[]>([])
  const [jobs, setJobs] = useState<SubtitleJob[]>([])
  const [history, setHistory] = useState<SubtitleHistoryEntry[]>([])
  const [profiles, setProfiles] = useState<SubtitleProfile[]>([])
  const [providers, setProviders] = useState<SubtitleProviderStatus[]>([])
  const [settingsLoaded, setSettingsLoaded] = useState(false)
  const [query, setQuery] = useState('')
  const [kind, setKind] = useState('')
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [feedback, setFeedback] = useState<{ tone: 'success' | 'error'; message: string; scope?: string } | null>(null)
  const [selected, setSelected] = useState<Target | null>(null)
  const [dismissed, setDismissed] = useState('')

  const writeReasonId = 'subtitles-write-reason'

  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!canRead) {
        setLoading(false)
        return
      }
      try {
        setLoading(true)
        const [library, wantedItems, jobList, entries] = await Promise.all([
          subtitlesApi.library({ q: query.trim(), kind, status }, signal),
          subtitlesApi.wanted(signal),
          subtitlesApi.jobs(false, signal),
          subtitlesApi.history({ limit: 60 }, signal),
        ])
        if (signal?.aborted) return
        setItems(library)
        setWanted(wantedItems)
        setJobs(jobList)
        setHistory(entries)
        setError('')
      } catch (cause) {
        if (!signal?.aborted) setError(errorMessage(cause))
      } finally {
        if (!signal?.aborted) setLoading(false)
      }
      // Profiles and providers are settings data; read-only subtitle roles must not request them.
      if (!canSettingsRead) return
      try {
        const [profileList, providerStatuses] = await Promise.all([subtitlesApi.profiles(signal), subtitlesApi.providers(signal)])
        if (signal?.aborted) return
        setProfiles(profileList)
        setProviders(providerStatuses)
        setSettingsLoaded(true)
      } catch {
        // Setup guidance stays hidden when settings data is unavailable.
        if (!signal?.aborted) setSettingsLoaded(false)
      }
    },
    [query, kind, status, canRead, canSettingsRead],
  )

  useEffect(() => {
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const hasActiveJobs = useMemo(() => jobs.some(isActive), [jobs])
  useEffect(() => {
    if (!hasActiveJobs) return
    const timer = window.setInterval(() => {
      load().catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [hasActiveJobs, load])

  const profileNames = useMemo(() => new Map(profiles.map((profile) => [profile.id, profile.name])), [profiles])
  const pendingReview = useMemo(() => items.reduce((total, item) => total + item.outputs.length, 0), [items])

  const setupGaps = useMemo(() => {
    if (!canSettingsRead || !settingsLoaded) return []
    const gaps: string[] = []
    if (profiles.length === 0) gaps.push('No language profile yet, so Constellarr does not know which subtitle languages to look for.')
    if (!providers.some((provider) => provider.configured && provider.enabled)) {
      gaps.push('No subtitle provider is connected, so no results can be found or downloaded.')
    }
    return gaps
  }, [canSettingsRead, settingsLoaded, profiles.length, providers])

  const targetKind = target?.kind ?? ''
  const targetId = target?.id ?? ''
  const targetMode = target?.mode ?? 'detail'
  // Memoize on primitive fields so an inline target prop keeps the same identity for the dialog.
  const externalTarget = useMemo<Target | null>(
    () => (targetKind && targetId ? { kind: targetKind, id: targetId, mode: targetMode } : null),
    [targetKind, targetId, targetMode],
  )
  const activeTarget = selected ?? (externalTarget && `${externalTarget.kind}:${externalTarget.id}` !== dismissed ? externalTarget : null)

  const closeDialog = () => {
    setSelected(null)
    if (externalTarget) setDismissed(`${externalTarget.kind}:${externalTarget.id}`)
  }

  const runScan = async () => {
    setError('')
    setFeedback(null)
    try {
      await subtitlesApi.scan()
      setFeedback({ tone: 'success', message: 'Looking for videos and subtitle files. Results appear here in a moment.' })
      window.setTimeout(() => load().catch(() => undefined), 1500)
    } catch (cause) {
      setFeedback({ tone: 'error', message: errorMessage(cause) })
    }
  }

  const cancelJob = async (id: string) => {
    setFeedback(null)
    try {
      await subtitlesApi.cancelJob(id)
      setFeedback({ tone: 'success', message: 'Job cancelled.', scope: id })
      await load()
    } catch (cause) {
      setFeedback({ tone: 'error', message: errorMessage(cause), scope: id })
    }
  }

  const clearFilters = () => {
    setQuery('')
    setKind('')
    setStatus('')
  }

  if (!canRead) {
    return (
      <div className="space-y-5">
        <PageHeading title="Subtitles" description="Subtitle files, languages, and reviewing." />
        <EmptyNote>
          Subtitle state requires the subtitles read permission. Ask an administrator for access.
        </EmptyNote>
      </div>
    )
  }

  const filtersActive = Boolean(query.trim() || kind || status)

  return (
    <div className="space-y-5">
      <PageHeading
        title="Subtitles"
        description="Find and manage subtitles for your movies and episodes, then review changes before they are saved."
        action={
          <div className="flex flex-col items-end gap-1.5">
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => load().catch((cause) => setError(errorMessage(cause)))}>
                <RefreshCwIcon data-icon="inline-start" />
                Refresh
              </Button>
              <Button
                size="sm"
                onClick={runScan}
                disabled={!canWrite}
                aria-describedby={canWrite ? undefined : writeReasonId}
                title={canWrite ? undefined : 'Requires the subtitles write permission'}
              >
                <ScanSearchIcon data-icon="inline-start" />
                Scan library
              </Button>
            </div>
            {feedback && !feedback.scope && <ActionNote tone={feedback.tone}>{feedback.message}</ActionNote>}
          </div>
        }
      />

      <TabButtons
        label="Subtitle sections"
        value={tab}
        onChange={setTab}
        items={tabs
          .filter((item) => item.value !== 'settings' || canSettingsRead)
          .map((item) => ({
            value: item.value,
            label: item.label,
            icon: item.icon,
            count:
              item.value === 'missing'
                ? wanted.length
                : item.value === 'activity'
                  ? jobs.filter(isActive).length
                  : item.value === 'library'
                    ? pendingReview
                    : 0,
          }))}
      />

      {error && <ErrorNote onRetry={() => load().catch((cause) => setError(errorMessage(cause)))}>{error}</ErrorNote>}
      {!canWrite && (
        <p id={writeReasonId} className="rounded-md border border-border px-3 py-2 text-xs text-muted-foreground">
          You can review subtitles, but searching, downloading, adjusting timing, translating, and extracting need the
          subtitles write permission.
        </p>
      )}
      {tab !== 'settings' && setupGaps.length > 0 && (
        <div className="space-y-2 rounded-lg border border-border bg-card p-4 text-sm">
          <p className="font-medium">Finish subtitle setup</p>
          <ul className="list-disc space-y-1 pl-5 text-muted-foreground">
            {setupGaps.map((gap) => (
              <li key={gap}>{gap}</li>
            ))}
          </ul>
          <Button size="sm" variant="outline" onClick={() => setTab('settings')}>
            Open subtitle settings
          </Button>
        </div>
      )}

      {tab === 'library' && (
        <div className="space-y-3">
          <div className="grid gap-2 sm:grid-cols-[minmax(0,2fr)_repeat(2,minmax(0,1fr))]">
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search titles"
              aria-label="Filter subtitles by title"
            />
            <Select value={kind} onChange={(event) => setKind(event.target.value)} aria-label="Filter by media kind">
              <option value="">Movies and episodes</option>
              <option value="movie">Movies</option>
              <option value="episode">Episodes</option>
            </Select>
            <Select value={status} onChange={(event) => setStatus(event.target.value)} aria-label="Filter by subtitle state">
              <option value="">Any subtitle state</option>
              <option value="missing">Missing subtitles</option>
              <option value="satisfied">Complete</option>
              <option value="unmonitored">Not monitored</option>
            </Select>
          </div>
          {loading && items.length === 0 ? (
            <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
              <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" /> Loading subtitles…
            </p>
          ) : items.length === 0 && filtersActive ? (
            <EmptyNote>
              <p className="font-medium text-foreground">No videos match this filter.</p>
              <p className="mt-1">Try another title or clear the filters to see the whole library.</p>
              <Button size="sm" variant="outline" className="mt-3" onClick={clearFilters}>
                Clear filters
              </Button>
            </EmptyNote>
          ) : items.length === 0 ? (
            <EmptyNote>
              <p className="font-medium text-foreground">Subtitle work starts once a video is in your library.</p>
              <p className="mt-1">
                Downloads that are still running or waiting to be imported do not appear here yet. Once a movie or episode
                finishes downloading and is imported, Constellarr can find subtitles for it, adjust their timing, and
                translate them.
              </p>
              <div className="mt-3 flex flex-wrap gap-2">
                <Button asChild size="sm" variant="outline">
                  <a href="#movies">
                    <FilmIcon data-icon="inline-start" />
                    Movies
                  </a>
                </Button>
                <Button asChild size="sm" variant="outline">
                  <a href="#tv-shows">
                    <TvIcon data-icon="inline-start" />
                    TV shows
                  </a>
                </Button>
              </div>
              {canWrite ? (
                <p className="mt-3">
                  Already imported something? Use <span className="text-foreground">Scan library</span> to look for videos
                  and subtitle files again.
                </p>
              ) : (
                <p className="mt-3">An administrator connects the subtitle provider and language profiles in Settings.</p>
              )}
            </EmptyNote>
          ) : (
            <ul className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {items.map((item) => (
                <LibraryCard
                  key={`${item.video.kind}-${item.video.id}`}
                  item={item}
                  canWrite={canWrite}
                  profileName={profileNames.get(item.profileId) ?? ''}
                  writeReasonId={writeReasonId}
                  onOpen={(mode) => setSelected({ kind: item.video.kind, id: item.video.id, mode })}
                />
              ))}
            </ul>
          )}
        </div>
      )}

      {tab === 'missing' && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Languages Constellarr is still looking for, and when it will try again.
          </p>
          {wanted.length === 0 ? (
            <EmptyNote>Nothing is missing. Every monitored video has the subtitle languages its profile asks for.</EmptyNote>
          ) : (
            <ul className="divide-y divide-border rounded-lg border border-border">
              {wanted.map((item) => (
                <li
                  key={`${item.video.kind}-${item.video.id}-${item.wanted.language}-${item.wanted.forced}-${item.wanted.hi}`}
                  className="flex flex-wrap items-center justify-between gap-3 p-3"
                >
                  <div className="min-w-0 space-y-1">
                    <p className="truncate text-sm font-medium">{videoTitle(item.video)}</p>
                    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                      <VariantBadges item={item.wanted} />
                      <span>{item.wanted.status === 'cutoff' ? 'searching stopped at the cutoff' : 'waiting to be found'}</span>
                      {item.wanted.attempts > 0 && (
                        <span>{`${item.wanted.attempts} ${item.wanted.attempts === 1 ? 'try' : 'tries'}`}</span>
                      )}
                      {item.wanted.nextAttemptAt && <span>next try {timeLabel(item.wanted.nextAttemptAt)}</span>}
                    </div>
                    {item.wanted.error && <p className="break-words text-xs text-destructive">{item.wanted.error}</p>}
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setSelected({ kind: item.video.kind, id: item.video.id, mode: 'search' })}
                    disabled={!canWrite}
                    aria-describedby={canWrite ? undefined : writeReasonId}
                  >
                    <ScanSearchIcon data-icon="inline-start" />
                    Find subtitles
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {tab === 'activity' && (
        <div className="grid gap-5 lg:grid-cols-2">
          <section className="space-y-3">
            <h3 className="font-heading text-sm font-semibold">Running and recent tasks</h3>
            {jobs.length === 0 ? (
              <EmptyNote>No subtitle tasks yet. Finding subtitles creates tasks you can follow here.</EmptyNote>
            ) : (
              <ul className="divide-y divide-border rounded-lg border border-border">
                {jobs.map((job) => (
                  <li key={job.id} className="space-y-1 p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                        <Badge variant={statusVariant(job.status)}>{job.status}</Badge>
                        {jobKindLabel(job.kind)}
                        {job.language && <Badge variant="outline">{languageName(job.language)}</Badge>}
                      </span>
                      {isActive(job) && (
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => cancelJob(job.id)}
                          disabled={!canWrite}
                          aria-describedby={canWrite ? undefined : writeReasonId}
                        >
                          Cancel
                        </Button>
                      )}
                    </div>
                    <p className="break-words text-xs text-muted-foreground">{job.detail || job.error || `${job.progress}%`}</p>
                    {job.error && <p className="break-words text-xs text-destructive">{job.error}</p>}
                    <p className="text-xs text-muted-foreground">{formatAge(job.createdAt)}</p>
                    {feedback?.scope === job.id && <ActionNote tone={feedback.tone}>{feedback.message}</ActionNote>}
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section className="space-y-3">
            <h3 className="font-heading text-sm font-semibold">History</h3>
            {history.length === 0 ? (
              <EmptyNote>Nothing has happened with subtitles yet.</EmptyNote>
            ) : (
              <ul className="divide-y divide-border rounded-lg border border-border">
                {history.map((entry) => (
                  <li key={entry.id} className="flex items-start gap-2 p-3 text-sm">
                    <Badge variant={statusVariant(entry.action)}>{historyActionLabel(entry.action)}</Badge>
                    <span className="min-w-0 flex-1">
                      <span className="break-words">{entry.message}</span>
                      <span className="block text-xs text-muted-foreground">
                        {entry.language && `${languageName(entry.language)} · `}
                        {timeLabel(entry.createdAt)}
                      </span>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      )}

      {tab === 'settings' && canSettingsRead && (
        <SubtitleSettings
          profiles={profiles}
          onChanged={() => {
            load().catch((cause) => setError(errorMessage(cause)))
          }}
        />
      )}

      <SubtitleDetailDialog
        target={activeTarget}
        profiles={profiles}
        providerReady={settingsLoaded ? providers.some((provider) => provider.configured && provider.enabled) : undefined}
        canWrite={canWrite}
        canSettingsRead={canSettingsRead}
        onClose={closeDialog}
        onChanged={() => {
          load().catch(() => undefined)
        }}
      />
    </div>
  )
}
