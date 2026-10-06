import { useEffect, useState } from 'react'
import {
  CalendarPlusIcon,
  CaptionsIcon,
  CheckIcon,
  ChevronDownIcon,
  CircleAlertIcon,
  FileDownIcon,
  LoaderCircleIcon,
  PencilIcon,
  RefreshCwIcon,
  SaveIcon,
  SearchIcon,
  Trash2Icon,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Checkbox,
  DialogShell,
  EmptyState,
  ErrorNote,
  LoadingNote,
  MetaRow,
  Notice,
  Poster,
  Progress,
  Section,
  Select,
  SeriesStatusBadge,
  StatusBadge,
} from '@/components/tv-ui'
import { ReleaseDialog } from '@/components/tv-release-dialog'
import { errorMessage } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'
import {
  activeStatuses,
  airDateValue,
  availableFiles,
  bestFile,
  episodeCode,
  episodeSubtitleHref,
  formatDate,
  formatDateTime,
  isAired,
  monitorModeLabel,
  monitorModes,
  ratingText,
  seasonLabel,
  splitList,
  strings,
  tagsOf,
} from '@/components/tv-shared'
import {
  tvApi,
  type Episode,
  type MovieProfile,
  type RenameResult,
  type RootFolder,
  type Series,
  type Target,
  type TvHistoryEntry,
} from '@/lib/tv-api'

function EpisodeFileLink({ seriesId, episode }: { seriesId: string; episode: Episode }) {
  const file = bestFile(episode)
  if (!file) return null
  return (
    <Button asChild size="xs" variant="ghost">
      <a
        href={tvApi.fileUrl(seriesId, file.path)}
        download
        aria-label={`Download ${episodeCode(episode)} ${episode.title || 'episode'} file`}
        title={file.path}
      >
        <FileDownIcon data-icon="inline-start" />
        File
      </a>
    </Button>
  )
}

// Subtitles need an imported file; aired episodes without one show the reason as text instead of a dead action.
function EpisodeSubtitleLink({ episode, canReadSubtitles }: { episode: Episode; canReadSubtitles: boolean }) {
  const file = bestFile(episode)
  if (!canReadSubtitles) return null
  if (!file && !isAired(episode)) return null
  const context = `${episodeCode(episode)} ${episode.title || 'episode'}`.trim()
  if (!file) {
    return <span className="text-xs text-muted-foreground">Subtitles need an episode file</span>
  }
  return (
    <Button asChild size="xs" variant="outline">
      <a
        href={episodeSubtitleHref(episode.id)}
        aria-label={`Subtitles for ${context}`}
        title={`Open subtitles for ${context}`}
      >
        <CaptionsIcon data-icon="inline-start" />
        Subtitles
      </a>
    </Button>
  )
}

export function SeriesDetailDialog({
  active,
  series,
  profiles,
  roots,
  canWrite,
  canReadSettings,
  canReadSubtitles,
  onClose,
  onSave,
  onReload,
  onRemove,
  onGrabbed,
  onChanged,
}: {
  active: boolean
  series: Series
  profiles: MovieProfile[]
  roots: RootFolder[]
  canWrite: boolean
  canReadSettings: boolean
  canReadSubtitles: boolean
  onClose: () => void
  onSave: (series: Series) => Promise<Series>
  onReload: () => void
  onRemove: (id: string, deleteFiles: boolean) => Promise<void>
  onGrabbed: (id: string) => void
  onChanged: () => void
}) {
  const [form, setForm] = useState(() => ({
    monitored: series.monitored,
    monitorMode: series.monitorMode || 'all',
    profileId: series.profileId,
    rootId: series.rootId,
    tags: tagsOf(series).join(', '),
  }))
  const [saving, setSaving] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [renaming, setRenaming] = useState<'preview' | 'apply' | null>(null)
  const [renameResult, setRenameResult] = useState<RenameResult | null>(null)
  const [removing, setRemoving] = useState(false)
  const [confirmRemove, setConfirmRemove] = useState(false)
  const [deleteFiles, setDeleteFiles] = useState(false)
  const [releaseTarget, setReleaseTarget] = useState<Target | null>(null)
  const [manualOpen, setManualOpen] = useState(false)
  const [manual, setManual] = useState({ season: '0', number: '', title: '', airDate: '' })
  const [manualBusy, setManualBusy] = useState(false)
  const [pendingEpisodes, setPendingEpisodes] = useState<Set<string>>(new Set())
  const [monitorBusy, setMonitorBusy] = useState('')
  const [selectedSeason, setSelectedSeason] = useState<'all' | number>('all')
  const [history, setHistory] = useState<TvHistoryEntry[] | null>(null)
  const [historyError, setHistoryError] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const tagsChanged = form.tags !== tagsOf(series).join(', ')
  const dirty =
    form.monitored !== series.monitored ||
    form.monitorMode !== (series.monitorMode || 'all') ||
    form.profileId !== series.profileId ||
    form.rootId !== series.rootId ||
    tagsChanged

  // Polling refreshes keep arriving; never discard an unsaved monitoring edit.
  useEffect(() => {
    if (dirty) return
    // eslint-disable-next-line react-hooks/set-state-in-effect -- only replaces a clean form after a refresh
    setForm({
      monitored: series.monitored,
      monitorMode: series.monitorMode || 'all',
      profileId: series.profileId,
      rootId: series.rootId,
      tags: tagsOf(series).join(', '),
    })
  }, [series, dirty])

  useEffect(() => {
    const controller = new AbortController()
    tvApi
      .history(series.id, controller.signal)
      .then((entries) => {
        if (!controller.signal.aborted) setHistory(entries)
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setHistoryError(errorMessage(cause))
      })
    return () => controller.abort()
  }, [series.id])

  const episodes = series.episodes ?? []
  const seasons = [...new Set(episodes.map((episode) => episode.season))].sort((a, b) => a - b)

  const save = async () => {
    setSaving(true)
    setError('')
    setNotice('')
    try {
      const saved = await onSave({
        ...series,
        monitored: form.monitored,
        monitorMode: form.monitorMode,
        profileId: form.profileId,
        rootId: form.rootId,
        tags: splitList(form.tags),
      })
      setForm({
        monitored: saved.monitored,
        monitorMode: saved.monitorMode || 'all',
        profileId: saved.profileId,
        rootId: saved.rootId,
        tags: tagsOf(saved).join(', '),
      })
      setNotice('Series settings saved.')
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
      await tvApi.refresh(series.id)
      onReload()
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
      const result = await tvApi.rename(series.id, preview)
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
      await onRemove(series.id, deleteFiles)
      onClose()
    } catch (cause) {
      setError(errorMessage(cause))
      setRemoving(false)
    }
  }

  const toggleEpisode = async (episode: Episode, monitored: boolean) => {
    setPendingEpisodes((previous) => new Set(previous).add(episode.id))
    setError('')
    setNotice('')
    try {
      await tvApi.monitor(series.id, { episodeIds: [episode.id], monitored })
      onReload()
      setNotice(
        monitored
          ? `Downloading ${episodeCode(episode)} automatically when a release matches.`
          : `Stopped automatic downloads for ${episodeCode(episode)}.`,
      )
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setPendingEpisodes((previous) => {
        const next = new Set(previous)
        next.delete(episode.id)
        return next
      })
    }
  }

  const monitorScope = async (scope: number | null, monitored: boolean, key: string) => {
    setMonitorBusy(key)
    setError('')
    setNotice('')
    try {
      await tvApi.monitor(series.id, scope === null ? { monitored } : { season: scope, monitored })
      onReload()
      setNotice(
        `${monitored ? 'Downloading' : 'Stopped downloading'} ${
          scope === null ? 'every episode' : scope === 0 ? 'specials' : `season ${scope}`
        } automatically.`,
      )
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setMonitorBusy('')
    }
  }

  const addManualEpisode = async () => {
    const season = Number(manual.season)
    const number = Number(manual.number)
    if (!Number.isInteger(season) || season < 0) {
      setError('Enter a season number of 0 or more (0 is specials).')
      return
    }
    if (!Number.isInteger(number) || number <= 0) {
      setError('Enter an episode number of 1 or more.')
      return
    }
    setManualBusy(true)
    setError('')
    setNotice('')
    try {
      await tvApi.addEpisode(series.id, {
        season,
        number,
        title: manual.title.trim() || undefined,
        airDate: manual.airDate.trim() || undefined,
      })
      setManual({ season: String(season), number: '', title: '', airDate: '' })
      onReload()
      onChanged()
      setNotice(`Added ${seasonLabel(season)} episode ${number}.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setManualBusy(false)
    }
  }

  const metadata = series.metadata
  const rating = ratingText(metadata.rating)
  const seasonEpisodes = (season: number) =>
    episodes.filter((episode) => episode.season === season).sort((a, b) => a.number - b.number)

  return (
    <DialogShell
      active={active}
      title={metadata.title || 'Series details'}
      description={`${metadata.year > 0 ? metadata.year : 'Year unknown'} · ${episodes.length} episodes · ${
        series.downloaded
      }/${series.total} downloaded`}
      onClose={onClose}
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
              <SeriesStatusBadge series={series} />
            </div>
            <p className="text-sm text-muted-foreground">
              {metadata.year > 0 ? metadata.year : 'Year unknown'}
              {metadata.totalSeasons && metadata.totalSeasons > 0 ? ` · ${metadata.totalSeasons} seasons` : ''}
              {metadata.imdbId ? ` · ${metadata.imdbId}` : ''}
              {` · Added ${formatDate(Date.parse(series.addedAt) || null)}`}
            </p>
            <p className={series.monitored ? 'text-xs text-muted-foreground' : 'text-xs text-amber-300'}>
              {series.monitored
                ? `Downloading automatically · ${monitorModeLabel(series.monitorMode || 'all')}`
                : 'Automatic downloads are off: nothing is searched or downloaded until you choose a release.'}
            </p>
            {rating && (
              <p className="text-sm">
                <span className="font-medium">{rating}</span>{' '}
                <span className="text-muted-foreground">
                  IMDb{metadata.votes > 0 ? ` from ${metadata.votes.toLocaleString()} votes` : ''}
                </span>
              </p>
            )}
            {strings(metadata.genres).length > 0 && (
              <p className="text-sm text-muted-foreground">{strings(metadata.genres).join(', ')}</p>
            )}
            {metadata.plot && <p className="text-sm text-muted-foreground">{metadata.plot}</p>}
            <div className="max-w-sm pt-1">
              <Progress series={series} />
            </div>
            {series.error && (
              <p role="alert" className="text-sm text-destructive">
                {series.error}
              </p>
            )}
            {series.lastRefreshAt && (
              <p className="text-xs text-muted-foreground">Metadata refreshed {formatAge(series.lastRefreshAt)}.</p>
            )}
          </div>
        </div>

        <details className="rounded-lg border border-border p-3">
          <summary className="cursor-pointer text-sm font-medium">Cast, languages, and other details</summary>
          <dl className="mt-3 grid gap-2 sm:grid-cols-2">
            <MetaRow label="Monitor mode">{monitorModeLabel(series.monitorMode || 'all')}</MetaRow>
            <MetaRow label="Tags">{tagsOf(series).join(', ') || 'None'}</MetaRow>
            <MetaRow label="Cast">{strings(metadata.cast).join(', ') || 'Unknown'}</MetaRow>
            <MetaRow label="Languages">{strings(metadata.languages).join(', ') || 'Unknown'}</MetaRow>
          </dl>
        </details>

        <Section
          title="Episodes"
          action={
            <div className="flex flex-wrap items-center gap-2">
              {canWrite && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={monitorBusy !== ''}
                  aria-label="Download every episode of this series automatically"
                  onClick={() => void monitorScope(null, true, 'all')}
                >
                  {monitorBusy === 'all' ? (
                    <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <CheckIcon data-icon="inline-start" />
                  )}
                  Download everything automatically
                </Button>
              )}
              {canWrite && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={monitorBusy !== ''}
                  aria-label="Stop downloading every episode of this series automatically"
                  onClick={() => void monitorScope(null, false, 'none')}
                >
                  Stop automatic downloads
                </Button>
              )}
              {seasons.length > 1 && (
                <Select
                  aria-label="Season selector"
                  value={String(selectedSeason)}
                  onChange={(event) =>
                    setSelectedSeason(event.target.value === 'all' ? 'all' : Number(event.target.value))
                  }
                >
                  <option value="all">All seasons</option>
                  {seasons.map((season) => (
                    <option key={season} value={season}>
                      {seasonLabel(season)}
                    </option>
                  ))}
                </Select>
              )}
            </div>
          }
        >
          {episodes.length === 0 ? (
            <EmptyState>
              No episodes yet. Refresh metadata, or add episodes manually below for specials and offline series.
            </EmptyState>
          ) : (
            <div className="space-y-2">
              {seasons.map((season) => {
                const list = seasonEpisodes(season)
                const downloaded = list.filter((episode) => availableFiles(episode).length > 0).length
                const wanted = list.filter((episode) => episode.monitored && availableFiles(episode).length === 0).length
                const allMonitored = list.every((episode) => episode.monitored)
                return (
                  <details
                    key={`${series.id}-${season}-${String(selectedSeason)}`}
                    open={selectedSeason === 'all' ? false : selectedSeason === season}
                    className="group rounded-lg border border-border"
                  >
                    <summary className="flex cursor-pointer flex-wrap items-center gap-2 p-3 text-sm">
                      <ChevronDownIcon
                        className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180"
                        aria-hidden="true"
                      />
                      <span className="font-medium">{seasonLabel(season)}</span>
                      <span className="text-xs text-muted-foreground tabular-nums">
                        {downloaded}/{list.length} downloaded
                        {wanted > 0 ? ` · ${wanted} wanted` : ''}
                      </span>
                      <span className="ml-auto flex flex-wrap items-center gap-1.5">
                        {canWrite && season > 0 && (
                          <Button
                            size="xs"
                            variant="outline"
                            aria-label={`Search releases for ${seasonLabel(season)}`}
                            onClick={(event) => {
                              event.preventDefault()
                              setReleaseTarget({ season, episode: 0 })
                            }}
                          >
                            <SearchIcon data-icon="inline-start" />
                            Search season
                          </Button>
                        )}
                        {canWrite && (
                          <Button
                            size="xs"
                            variant="ghost"
                            disabled={monitorBusy !== ''}
                            aria-pressed={allMonitored}
                            aria-label={
                              allMonitored
                                ? `Stop downloading ${seasonLabel(season)} automatically`
                                : `Download ${seasonLabel(season)} automatically`
                            }
                            title={
                              allMonitored
                                ? `Stop searching and downloading ${seasonLabel(season)} automatically.`
                                : `Search indexers and download ${seasonLabel(season)} automatically.`
                            }
                            onClick={(event) => {
                              event.preventDefault()
                              void monitorScope(season, !allMonitored, `season-${season}`)
                            }}
                          >
                            {monitorBusy === `season-${season}` ? (
                              <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                            ) : (
                              'Automatic downloads'
                            )}
                          </Button>
                        )}
                      </span>
                    </summary>
                    <ul className="divide-y divide-border border-t border-border">
                      {list.map((episode) => {
                        const file = bestFile(episode)
                        const aired = isAired(episode)
                        const airAt = airDateValue(episode.airDate)
                        const episodeRating = ratingText(episode.rating)
                        const pending = pendingEpisodes.has(episode.id)
                        return (
                          <li key={episode.id} className="flex flex-wrap items-center gap-2 p-2.5">
                            <input
                              type="checkbox"
                              className="size-4 accent-primary"
                              checked={episode.monitored}
                              disabled={pending || !canWrite}
                              onChange={(event) => void toggleEpisode(episode, event.target.checked)}
                              aria-label={`Download ${episodeCode(episode)} automatically when a release matches`}
                            />
                            <span className="w-10 shrink-0 text-xs text-muted-foreground tabular-nums">
                              E{String(episode.number).padStart(2, '0')}
                            </span>
                            <div className="min-w-0 flex-1">
                              <p className="truncate text-sm">{episode.title || 'Untitled episode'}</p>
                              <p className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
                                <span>
                                  {airAt === null
                                    ? 'Air date unknown'
                                    : `${formatDate(airAt)} · ${aired ? 'Aired' : 'Upcoming'}`}
                                </span>
                                {episodeRating && <span>{episodeRating} IMDb</span>}
                                <span>
                                  {file
                                    ? `Downloaded · ${file.quality || 'quality unknown'} · ${formatBytes(file.size)}`
                                    : aired
                                      ? 'Awaiting video'
                                      : 'Not aired yet'}
                                </span>
                                {episode.lastSearchAt && <span>Last search {formatAge(episode.lastSearchAt)}</span>}
                              </p>
                              {episode.error && <p className="text-xs text-destructive">{episode.error}</p>}
                            </div>
                            {episode.status && (!file || episode.status === 'cutoff-unmet') && <StatusBadge status={episode.status} />}
                            {activeStatuses.has(episode.status) && file && <StatusBadge status={episode.status} />}
                            <EpisodeFileLink seriesId={series.id} episode={episode} />
                            <EpisodeSubtitleLink episode={episode} canReadSubtitles={canReadSubtitles} />
                            {canWrite && (
                              <Button
                                size="xs"
                                variant="outline"
                                aria-label={`Search releases for ${episodeCode(episode)} ${episode.title || 'episode'}`}
                                onClick={() => setReleaseTarget({ season: episode.season, episode: episode.number })}
                              >
                                <SearchIcon data-icon="inline-start" />
                                Search releases
                              </Button>
                            )}
                          </li>
                        )
                      })}
                    </ul>
                  </details>
                )
              })}
            </div>
          )}
          {canWrite && (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                aria-label={`Search releases for the whole ${metadata.title || 'series'}`}
                onClick={() => setReleaseTarget({ season: -1, episode: 0 })}
              >
                <SearchIcon data-icon="inline-start" />
                Search whole series
              </Button>
            </div>
          )}
        </Section>

        {canWrite && (
          <Section
            title="Manual episode"
            action={
              <Button
                size="sm"
                variant="ghost"
                aria-expanded={manualOpen}
                aria-controls="tv-manual-episode"
                onClick={() => setManualOpen((current) => !current)}
              >
                {manualOpen ? 'Hide' : 'Add episode'}
              </Button>
            }
          >
            {manualOpen ? (
              <div id="tv-manual-episode" className="space-y-3 rounded-lg border border-border p-3">
                <p className="text-xs text-muted-foreground">
                  Use this for specials or offline series, then search releases for the episode like any other. Season
                  0 is the specials season.
                </p>
              <div className="grid gap-3 sm:grid-cols-4">
                <div className="space-y-2">
                  <label htmlFor="tv-manual-season" className="text-sm font-medium">
                    Season
                  </label>
                  <Input
                    id="tv-manual-season"
                    type="number"
                    min="0"
                    value={manual.season}
                    onChange={(event) => setManual({ ...manual, season: event.target.value })}
                  />
                </div>
                <div className="space-y-2">
                  <label htmlFor="tv-manual-number" className="text-sm font-medium">
                    Episode
                  </label>
                  <Input
                    id="tv-manual-number"
                    type="number"
                    min="1"
                    value={manual.number}
                    onChange={(event) => setManual({ ...manual, number: event.target.value })}
                  />
                </div>
                <div className="space-y-2 sm:col-span-2">
                  <label htmlFor="tv-manual-title" className="text-sm font-medium">
                    Title
                  </label>
                  <Input
                    id="tv-manual-title"
                    value={manual.title}
                    onChange={(event) => setManual({ ...manual, title: event.target.value })}
                  />
                </div>
                <div className="space-y-2 sm:col-span-2">
                  <label htmlFor="tv-manual-airdate" className="text-sm font-medium">
                    Air date
                  </label>
                  <Input
                    id="tv-manual-airdate"
                    type="date"
                    value={manual.airDate}
                    onChange={(event) => setManual({ ...manual, airDate: event.target.value })}
                  />
                </div>
              </div>
              <div className="flex justify-end">
                <Button size="sm" disabled={manualBusy} onClick={() => void addManualEpisode()}>
                  {manualBusy ? (
                    <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <CalendarPlusIcon data-icon="inline-start" />
                  )}
                  Add episode
                </Button>
              </div>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              Specials and unmatched episodes can be added manually, then searched and imported like any other episode.
            </p>
          )}
        </Section>
        )}

        <Section
          title="Downloads and organization"
          action={
            canWrite ? (
              <span className="text-xs text-muted-foreground" role="status">
                {dirty ? 'Unsaved changes' : 'Saved'}
              </span>
            ) : undefined
          }
        >
          {canWrite ? (
            <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Checkbox
              id="tv-detail-monitored"
              label="Download automatically"
              description="Constellarr searches indexers and downloads releases for this series. Off keeps missing episodes in Wanted until you choose a release."
              checked={form.monitored}
              onChange={(monitored) => setForm({ ...form, monitored })}
            />
            <div className="space-y-2">
              <label htmlFor="tv-detail-mode" className="text-sm font-medium">
                Episodes to download
              </label>
              <Select
                id="tv-detail-mode"
                className="w-full"
                value={form.monitorMode}
                disabled={!form.monitored}
                onChange={(event) => setForm({ ...form, monitorMode: event.target.value })}
              >
                {monitorModes.map((mode) => (
                  <option key={mode.value} value={mode.value}>
                    {mode.label}
                  </option>
                ))}
              </Select>
            </div>
          </div>
          <p className={form.monitored ? 'text-xs text-muted-foreground' : 'text-xs text-amber-300'} role="status">
            {form.monitored
              ? 'Missing episodes in this selection are searched automatically.'
              : 'Nothing is searched or downloaded while automatic downloads are off.'}
          </p>
          <details className="rounded-lg border border-border p-3">
            <summary className="cursor-pointer text-sm font-medium">Quality profile, root folder, and tags</summary>
            <div className="mt-3 grid gap-4 sm:grid-cols-2">
              {canReadSettings && (
                <div className="space-y-2">
                  <label htmlFor="tv-detail-profile" className="text-sm font-medium">
                    Quality profile
                  </label>
                  <Select
                    id="tv-detail-profile"
                    className="w-full"
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
                  <label htmlFor="tv-detail-root" className="text-sm font-medium">
                    Root folder
                  </label>
                  <Select
                    id="tv-detail-root"
                    className="w-full"
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
              <div className="space-y-2 sm:col-span-2">
                <label htmlFor="tv-detail-tags" className="text-sm font-medium">
                  Tags
                </label>
                <Input
                  id="tv-detail-tags"
                  value={form.tags}
                  placeholder="kids, anime"
                  onChange={(event) => setForm({ ...form, tags: event.target.value })}
                />
              </div>
            </div>
            {!canReadSettings ? (
              <p className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
                <CircleAlertIcon className="size-4 shrink-0" />
                The current root folder and quality profile stay as configured on the server.
              </p>
            ) : (
              (profiles.length === 0 || roots.length === 0) && (
                <p className="mt-3 flex items-center gap-2 text-xs text-amber-300">
                  <CircleAlertIcon className="size-4 shrink-0" />
                  {roots.length === 0 ? (
                    <>
                      Add a TV root folder in{' '}
                      <a href="#storage" className="underline underline-offset-4">
                        Storage & Paths
                      </a>
                      .
                    </>
                  ) : (
                    'Create a shared quality profile in Movies → Profiles.'
                  )}
                </p>
              )
            )}
          </details>
          <div className="flex justify-end">
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
              <MetaRow label="Automatic downloads">{form.monitored ? 'On' : 'Off'}</MetaRow>
              <MetaRow label="Episodes to download">{monitorModeLabel(series.monitorMode || 'all')}</MetaRow>
              <MetaRow label="Quality profile">
                {profiles.find((profile) => profile.id === series.profileId)?.name ||
                  (series.profileId ? 'Configured profile' : 'Default')}
              </MetaRow>
              <MetaRow label="Root folder">
                {roots.find((root) => root.id === series.rootId)?.path ||
                  (series.rootId ? 'Configured root folder' : 'Default')}
              </MetaRow>
              <MetaRow label="Tags">{tagsOf(series).join(', ') || 'None'}</MetaRow>
              <p className="text-xs text-muted-foreground sm:col-span-2">
                Editing series settings requires library write access.
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
              {refreshing ? 'Refreshing…' : 'Refresh metadata'}
            </Button>
            <Button size="sm" variant="outline" disabled={renaming !== null} onClick={() => void runRename(true)}>
              {renaming === 'preview' ? (
                <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
              ) : (
                <PencilIcon data-icon="inline-start" />
              )}
              Rename preview
            </Button>
            {confirmRemove ? (
              <span className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-1.5">
                <span className="flex flex-col gap-1 text-xs text-muted-foreground">
                  <span className="text-sm">Remove from catalog?</span>
                  <span>Imported files stay on disk unless you recycle them.</span>
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={deleteFiles}
                      onChange={(event) => setDeleteFiles(event.target.checked)}
                    />
                    Move episode files to .recycle
                  </label>
                </span>
                <Button size="sm" variant="outline" onClick={() => setConfirmRemove(false)}>
                  Cancel
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={removing}
                  aria-label={`Remove ${metadata.title || 'series'} from the catalog`}
                  onClick={() => void remove()}
                >
                  {removing ? (
                    <LoaderCircleIcon className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <Trash2Icon />
                  )}
                  Remove
                </Button>
              </span>
            ) : (
              <Button
                size="sm"
                variant="outline"
                className="text-destructive"
                aria-expanded={confirmRemove}
                onClick={() => setConfirmRemove(true)}
              >
                <Trash2Icon data-icon="inline-start" />
                Remove from catalog
              </Button>
            )}
          </div>

          {renameResult && (
            <div className="space-y-2 rounded-lg border border-border p-3">
              <p className="text-sm font-medium">
                {renameResult.applied ? 'Renamed files' : 'Rename preview, nothing changed yet'}
              </p>
              {renameResult.files.length === 0 ? (
                <p className="text-xs text-muted-foreground">No files to rename.</p>
              ) : (
                <details>
                  <summary className="cursor-pointer text-xs text-muted-foreground">
                    {renameResult.files.length} file path{renameResult.files.length === 1 ? '' : 's'}
                  </summary>
                  <ul className="mt-2 space-y-1 text-xs">
                    {renameResult.files.map((file, index) => (
                      <li key={`${file.from}-${index}`} className="break-all">
                        <span className="text-muted-foreground">{file.from}</span>
                        {' → '}
                        <span>{file.to}</span>
                      </li>
                    ))}
                  </ul>
                </details>
              )}
              {!renameResult.applied && renameResult.files.length > 0 && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={renaming !== null}
                  aria-label={`Rename ${renameResult.files.length} series files on disk`}
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

        <details className="rounded-lg border border-border p-3">
          <summary className="cursor-pointer text-sm font-medium">
            History{history !== null && history.length > 0 ? ` (${history.length})` : ''}
          </summary>
          <div className="mt-3">
            {historyError ? (
              <p role="alert" className="text-sm text-destructive">
                {historyError}
              </p>
            ) : history === null ? (
              <LoadingNote>Loading history…</LoadingNote>
            ) : history.length === 0 ? (
              <EmptyState>No history for this series yet.</EmptyState>
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
          </div>
        </details>

        {error && <ErrorNote>{error}</ErrorNote>}
        {notice && <Notice>{notice}</Notice>}
      </div>

      {releaseTarget && (
        <ReleaseDialog
          key={`${releaseTarget.season}-${releaseTarget.episode}`}
          active={active}
          seriesId={series.id}
          seriesTitle={metadata.title}
          target={releaseTarget}
          onClose={() => setReleaseTarget(null)}
          onGrabbed={onGrabbed}
        />
      )}
    </DialogShell>
  )
}
