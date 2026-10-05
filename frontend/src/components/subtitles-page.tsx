import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  ActivityIcon,
  CircleAlertIcon,
  LanguagesIcon,
  LoaderCircleIcon,
  RefreshCwIcon,
  ScanSearchIcon,
  SettingsIcon,
  Wand2Icon,
} from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { useAuth } from '@/lib/auth-context'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SubtitleDetailDialog } from '@/components/subtitles-detail'
import { SubtitleSettings } from '@/components/subtitles-settings'
import { EmptyNote, ErrorNote, Notice, Select, VariantBadges } from '@/components/subtitles-ui'
import { errorMessage } from '@/lib/api'
import { formatAge } from '@/lib/format'
import {
  statusVariant,
  subtitlesApi,
  timeLabel,
  type SubtitleHistoryEntry,
  type SubtitleJob,
  type SubtitleLibraryItem,
  type SubtitleProfile,
  type SubtitleWantedItem,
} from '@/lib/subtitles-api'

type Tab = 'library' | 'wanted' | 'activity' | 'settings'

const tabs: { value: Tab; label: string; icon: typeof LanguagesIcon }[] = [
  { value: 'library', label: 'Library', icon: LanguagesIcon },
  { value: 'wanted', label: 'Wanted', icon: CircleAlertIcon },
  { value: 'activity', label: 'Activity', icon: ActivityIcon },
  { value: 'settings', label: 'Settings', icon: SettingsIcon },
]

const activeJobStatuses = new Set(['queued', 'running'])

function isActive(job: SubtitleJob) {
  return activeJobStatuses.has(job.status)
}

function VideoLabel({ item }: { item: SubtitleLibraryItem }) {
  const video = item.video
  return (
    <div className="min-w-0">
      <p className="truncate font-medium">{video.seriesTitle ? `${video.seriesTitle} — ${video.title}` : video.title}</p>
      <p className="truncate text-xs text-muted-foreground">
        {[
          video.year > 0 ? String(video.year) : '',
          video.kind === 'episode' ? `S${video.season ?? 0}E${video.episode ?? 0}` : 'Movie',
          video.imdbId || video.seriesImdbId || '',
          video.path.split('/').pop() ?? '',
        ]
          .filter(Boolean)
          .join(' · ')}
      </p>
    </div>
  )
}

function LibraryCard({
  item,
  canWrite,
  onOpen,
}: {
  item: SubtitleLibraryItem
  canWrite: boolean
  onOpen: (mode: 'detail' | 'search') => void
}) {
  const missing = item.wanted.filter((row) => row.status === 'wanted')
  return (
    <li className="flex flex-col gap-3 rounded-lg border border-border bg-card p-3">
      <div className="flex items-start justify-between gap-2">
        <VideoLabel item={item} />
        <Badge variant={item.missing > 0 ? 'destructive' : 'secondary'}>
          {item.missing > 0 ? `${item.missing} missing` : 'complete'}
        </Badge>
      </div>
      <div className="flex flex-wrap items-center gap-1">
        {item.sidecars.length === 0 && <span className="text-xs text-muted-foreground">No subtitle files</span>}
        {item.sidecars.map((sidecar) => (
          <span key={sidecar.path} className="flex items-center gap-1">
            <VariantBadges item={sidecar} />
          </span>
        ))}
        {item.outputs.length > 0 && <Badge variant="outline">{item.outputs.length} awaiting review</Badge>}
      </div>
      <dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <div>
          <dt className="inline">Profile: </dt>
          <dd className="inline">{item.profileId || 'default'}</dd>
        </div>
        <div>
          <dt className="inline">Monitored: </dt>
          <dd className="inline">{item.monitored ? 'yes' : 'no'}</dd>
        </div>
        {missing.length > 0 && (
          <div className="min-w-0">
            <dt className="inline">Missing: </dt>
            <dd className="inline">{missing.map((row) => row.language + (row.forced ? ' forced' : '') + (row.hi ? ' HI' : '')).join(', ')}</dd>
          </div>
        )}
      </dl>
      <div className="flex flex-wrap gap-2">
        {canWrite && (
          <Button size="sm" variant="outline" onClick={() => onOpen('search')}>
            <ScanSearchIcon data-icon="inline-start" />
            Find
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={() => onOpen('detail')}>
          <Wand2Icon data-icon="inline-start" />
          {canWrite ? 'Manage' : 'View'}
        </Button>
      </div>
    </li>
  )
}

export function SubtitlesPage() {
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
  const [query, setQuery] = useState('')
  const [kind, setKind] = useState('')
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [selected, setSelected] = useState<{ kind: string; id: string; mode: 'detail' | 'search' } | null>(null)

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
        // Profiles are settings data; read-only subtitle roles must not request them.
        if (canSettingsRead) {
          const profileList = await subtitlesApi.profiles(signal)
          if (!signal?.aborted) setProfiles(profileList)
        }
        setError('')
      } catch (cause) {
        if (!signal?.aborted) setError(errorMessage(cause))
      } finally {
        if (!signal?.aborted) setLoading(false)
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

  const runScan = async () => {
    setNotice('')
    setError('')
    try {
      await subtitlesApi.scan()
      setNotice('Inventory scan started; results appear shortly.')
      window.setTimeout(() => load().catch(() => undefined), 1500)
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const cancelJob = async (id: string) => {
    try {
      await subtitlesApi.cancelJob(id)
      await load()
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  if (!canRead) {
    return (
      <div className="space-y-5">
        <PageHeading title="Subtitles" description="Subtitle inventory and automation." />
        <EmptyNote>
          Subtitle inventory requires the subtitles read permission. Ask an administrator for access.
        </EmptyNote>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <PageHeading
        title="Subtitles"
        description="Inventory sidecars, search providers, synchronize timing, and translate with review."
        action={
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => load().catch((cause) => setError(errorMessage(cause)))}>
              <RefreshCwIcon data-icon="inline-start" />
              Refresh
            </Button>
            {canWrite && (
              <Button size="sm" onClick={runScan}>
                <ScanSearchIcon data-icon="inline-start" />
                Scan library
              </Button>
            )}
          </div>
        }
      />

      <div className="flex flex-wrap gap-1 rounded-lg border border-border p-1">
        {tabs.filter((item) => item.value !== 'settings' || canSettingsRead).map((item) => {
          const Icon = item.icon
          const count = item.value === 'wanted' ? wanted.length : item.value === 'activity' ? jobs.filter(isActive).length : 0
          return (
            <Button
              key={item.value}
              size="sm"
              variant={tab === item.value ? 'secondary' : 'ghost'}
              onClick={() => setTab(item.value)}
              aria-pressed={tab === item.value}
            >
              <Icon data-icon="inline-start" />
              {item.label}
              {count > 0 && <Badge variant="outline">{count}</Badge>}
            </Button>
          )
        })}
      </div>

      {error && <ErrorNote onRetry={() => load().catch((cause) => setError(errorMessage(cause)))}>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}
      {!canWrite && (
        <p className="rounded-md border border-border px-3 py-2 text-xs text-muted-foreground">
          Your role can view subtitle state; searching, downloading, synchronizing, and translating require the subtitles
          write permission.
        </p>
      )}

      {tab === 'library' && (
        <div className="space-y-3">
          <div className="grid gap-2 sm:grid-cols-[minmax(0,2fr)_repeat(2,minmax(0,1fr))]">
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Filter by title, path, or IMDb ID"
              aria-label="Filter subtitles by title"
            />
            <Select value={kind} onChange={(event) => setKind(event.target.value)} aria-label="Filter by media kind">
              <option value="">Movies and episodes</option>
              <option value="movie">Movies</option>
              <option value="episode">Episodes</option>
            </Select>
            <Select value={status} onChange={(event) => setStatus(event.target.value)} aria-label="Filter by subtitle status">
              <option value="">Any status</option>
              <option value="missing">Missing subtitles</option>
              <option value="satisfied">Complete</option>
              <option value="unmonitored">Unmonitored</option>
            </Select>
          </div>
          {loading && items.length === 0 ? (
            <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
              <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" /> Loading subtitle library…
            </p>
          ) : items.length === 0 ? (
            <EmptyNote>No catalog videos match this filter. Add movies or episodes first, then scan the library.</EmptyNote>
          ) : (
            <ul className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {items.map((item) => (
                <LibraryCard
                  key={`${item.video.kind}-${item.video.id}`}
                  item={item}
                  canWrite={canWrite}
                  onOpen={(mode) => setSelected({ kind: item.video.kind, id: item.video.id, mode })}
                />
              ))}
            </ul>
          )}
        </div>
      )}

      {tab === 'wanted' && (
        <div className="space-y-3">
          {wanted.length === 0 ? (
            <EmptyNote>Every monitored video has all required subtitle languages.</EmptyNote>
          ) : (
            <ul className="divide-y divide-border rounded-lg border border-border">
              {wanted.map((item) => (
                <li key={`${item.video.kind}-${item.video.id}-${item.wanted.language}-${item.wanted.forced}-${item.wanted.hi}`} className="flex flex-wrap items-center justify-between gap-3 p-3">
                  <div className="min-w-0 space-y-1">
                    <p className="truncate text-sm font-medium">
                      {item.video.seriesTitle ? `${item.video.seriesTitle} — ${item.video.title}` : item.video.title}
                    </p>
                    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                      <VariantBadges item={item.wanted} />
                      <span>{item.wanted.status === 'cutoff' ? 'cutoff met' : 'wanted'}</span>
                      {item.wanted.attempts > 0 && <span>{item.wanted.attempts} attempts</span>}
                      {item.wanted.nextAttemptAt && <span>next {timeLabel(item.wanted.nextAttemptAt)}</span>}
                    </div>
                    {item.wanted.error && <p className="text-xs text-destructive">{item.wanted.error}</p>}
                  </div>
                  {canWrite && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => setSelected({ kind: item.video.kind, id: item.video.id, mode: 'search' })}
                    >
                      <ScanSearchIcon data-icon="inline-start" />
                      Find
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {tab === 'activity' && (
        <div className="grid gap-5 lg:grid-cols-2">
          <section className="space-y-3">
            <h3 className="font-heading text-sm font-semibold">Jobs</h3>
            {jobs.length === 0 ? (
              <EmptyNote>No subtitle jobs yet.</EmptyNote>
            ) : (
              <ul className="divide-y divide-border rounded-lg border border-border">
                {jobs.map((job) => (
                  <li key={job.id} className="space-y-1 p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="flex items-center gap-2 text-sm font-medium">
                        <Badge variant={statusVariant(job.status)}>{job.status}</Badge>
                        {job.kind}
                        {job.language && <Badge variant="outline">{job.language}</Badge>}
                      </span>
                      {isActive(job) && canWrite && (
                        <Button size="xs" variant="ghost" onClick={() => cancelJob(job.id)}>
                          Cancel
                        </Button>
                      )}
                    </div>
                    <p className="text-xs text-muted-foreground">{job.detail || job.error || `${job.progress}%`}</p>
                    {job.error && <p className="break-words text-xs text-destructive">{job.error}</p>}
                    <p className="text-xs text-muted-foreground">{formatAge(job.createdAt)}</p>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section className="space-y-3">
            <h3 className="font-heading text-sm font-semibold">History</h3>
            {history.length === 0 ? (
              <EmptyNote>No subtitle history yet.</EmptyNote>
            ) : (
              <ul className="divide-y divide-border rounded-lg border border-border">
                {history.map((entry) => (
                  <li key={entry.id} className="flex items-start gap-2 p-3 text-sm">
                    <Badge variant={statusVariant(entry.action)}>{entry.action}</Badge>
                    <span className="min-w-0 flex-1">
                      <span className="break-words">{entry.message}</span>
                      <span className="block text-xs text-muted-foreground">
                        {entry.language && `${entry.language} · `}
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
        target={selected}
        profiles={profiles}
        canWrite={canWrite}
        onClose={() => setSelected(null)}
        onChanged={() => {
          load().catch(() => undefined)
        }}
      />
    </div>
  )
}
