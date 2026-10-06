import { useCallback, useEffect, useRef, useState } from 'react'
import {
  BanIcon,
  CaptionsIcon,
  DownloadIcon,
  FileDownIcon,
  LanguagesIcon,
  LoaderCircleIcon,
  PlayIcon,
  RefreshCwIcon,
  SaveIcon,
  ScanSearchIcon,
  TimerIcon,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  ActionNote,
  DialogShell,
  Disclosure,
  EmptyNote,
  ErrorNote,
  Field,
  Section,
  Select,
  TabButtons,
  Toggle,
  VariantBadges,
  languageName,
  languageOptions,
  sourceLabel,
  variantText,
  videoMeta,
  videoTitle,
} from '@/components/subtitles-ui'
import { errorMessage } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import {
  statusVariant,
  subtitlesApi,
  timeLabel,
  type SubtitleDetail,
  type SubtitleJob,
  type SubtitleOutput,
  type SubtitleProfile,
  type SubtitleResult,
  type SubtitleSidecar,
  type SubtitleStream,
} from '@/lib/subtitles-api'

type Target = { kind: string; id: string; mode: 'detail' | 'search' } | null
type Task = 'subtitles' | 'find' | 'timing' | 'translate' | 'extract'
type Feedback = { scope: string; tone: 'success' | 'error' | 'warning'; message: string }

const tasks: { value: Task; label: string; icon: typeof CaptionsIcon }[] = [
  { value: 'subtitles', label: 'Subtitles', icon: CaptionsIcon },
  { value: 'find', label: 'Find subtitles', icon: ScanSearchIcon },
  { value: 'timing', label: 'Adjust timing', icon: TimerIcon },
  { value: 'translate', label: 'Translate', icon: LanguagesIcon },
  { value: 'extract', label: 'Extract from video', icon: FileDownIcon },
]

const activeJobStatuses = new Set(['queued', 'running'])

const syncModes: { value: 'offset' | 'fps' | 'audio' | 'reference'; label: string }[] = [
  { value: 'offset', label: 'Shift by a fixed offset' },
  { value: 'fps', label: 'Convert frame rate' },
  { value: 'audio', label: 'Match to the audio' },
  { value: 'reference', label: 'Match to another subtitle' },
]

function summarise(text: string, lines = 12) {
  const parts = text.split('\n')
  return parts.slice(0, lines).join('\n') + (parts.length > lines ? '\n…' : '')
}

function sidecarLabel(sidecar: SubtitleSidecar) {
  return `${variantText(sidecar)} · ${sidecar.format.toUpperCase()}`
}

function subtitleStreamLabel(stream: SubtitleStream) {
  return `${variantText(stream)} · track ${stream.index}`
}

function audioStreamLabel(stream: SubtitleStream) {
  return `${languageName(stream.language)}${stream.channels ? ` · ${stream.channels} channels` : ''} · track ${stream.index}`
}

function JobProgress({ job, canWrite, onCancel }: { job: SubtitleJob; canWrite: boolean; onCancel: () => void }) {
  return (
    <div role="status" className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
      <Badge variant={statusVariant(job.status)}>{job.status}</Badge>
      <span className="min-w-0 break-words">{job.detail || job.error || `${job.progress}%`}</span>
      {activeJobStatuses.has(job.status) && canWrite && (
        <Button size="xs" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      )}
    </div>
  )
}

export function SubtitleDetailDialog({
  target,
  profiles,
  canWrite,
  canSettingsRead,
  providerReady,
  onClose,
  onChanged,
}: {
  target: Target
  profiles: SubtitleProfile[]
  canWrite: boolean
  canSettingsRead: boolean
  providerReady?: boolean
  onClose: () => void
  onChanged: () => void
}) {
  const [task, setTask] = useState<Task>('subtitles')
  const [detail, setDetail] = useState<SubtitleDetail | null>(null)
  const [streams, setStreams] = useState<SubtitleStream[]>([])
  const [streamsError, setStreamsError] = useState('')
  const [streamsLoading, setStreamsLoading] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [feedback, setFeedback] = useState<Feedback | null>(null)
  const [job, setJob] = useState<{ job: SubtitleJob; task: Task } | null>(null)
  const [results, setResults] = useState<SubtitleResult[]>([])
  const [warnings, setWarnings] = useState<string[]>([])
  const [searching, setSearching] = useState(false)
  const [previewID, setPreviewID] = useState('')
  const [profileID, setProfileID] = useState('')
  const [monitored, setMonitored] = useState(true)

  const [syncPath, setSyncPath] = useState('')
  const [syncMode, setSyncMode] = useState<'offset' | 'fps' | 'audio' | 'reference'>('offset')
  const [offset, setOffset] = useState('0')
  const [fpsFrom, setFpsFrom] = useState('25')
  const [fpsTo, setFpsTo] = useState('23.976')
  const [referencePath, setReferencePath] = useState('')
  const [audioStream, setAudioStream] = useState('-1')
  const [maxOffset, setMaxOffset] = useState('')
  const [minScore, setMinScore] = useState('')
  const [noFixFramerate, setNoFixFramerate] = useState(false)
  const [goldenSection, setGoldenSection] = useState(false)
  const [syncPreview, setSyncPreview] = useState(false)

  const [translatePath, setTranslatePath] = useState('')
  const [translateLanguage, setTranslateLanguage] = useState('')
  const [extractStream, setExtractStream] = useState('-1')
  const [extractLanguage, setExtractLanguage] = useState('')
  const [extractPreview, setExtractPreview] = useState(false)

  const cancelled = useRef(false)
  const outputsRef = useRef(0)
  useEffect(() => {
    cancelled.current = false
    return () => {
      cancelled.current = true
    }
  }, [])

  const reload = useCallback(async () => {
    if (!target) return undefined
    const next = await subtitlesApi.detail(target.kind, target.id)
    setDetail(next)
    outputsRef.current = next.outputs.length
    return next
  }, [target])

  const load = useCallback(
    async (active: { kind: string; id: string; mode: 'detail' | 'search' }, signal?: AbortSignal) => {
      setTask(active.mode === 'search' ? 'find' : 'subtitles')
      try {
        setLoading(true)
        setFeedback(null)
        setJob(null)
        setResults([])
        setWarnings([])
        setPreviewID('')
        const loaded = await subtitlesApi.detail(active.kind, active.id, signal)
        if (signal?.aborted) return
        setDetail(loaded)
        outputsRef.current = loaded.outputs.length
        setProfileID(loaded.profileId)
        setMonitored(loaded.monitored)
        const first = loaded.sidecars[0]
        setSyncPath(first?.path ?? '')
        setTranslatePath(first?.path ?? '')
        setReferencePath(loaded.sidecars.find((item) => item.path !== first?.path)?.path ?? '')
        setTranslateLanguage(
          (loaded.wanted ?? []).map((row) => row.language).find((code) => code && code !== first?.language) ?? '',
        )
        setError('')
      } catch (cause) {
        if (!signal?.aborted) setError(errorMessage(cause))
      } finally {
        if (!signal?.aborted) setLoading(false)
      }
      try {
        setStreamsLoading(true)
        const list = await subtitlesApi.streams(active.kind, active.id, signal)
        if (!signal?.aborted) {
          setStreams(list)
          setStreamsError('')
        }
      } catch (cause) {
        if (!signal?.aborted) setStreamsError(errorMessage(cause))
      } finally {
        if (!signal?.aborted) setStreamsLoading(false)
      }
    },
    [],
  )

  useEffect(() => {
    if (!target) return
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void load(target, controller.signal)
    return () => controller.abort()
  }, [target, load])

  const noteFor = (scope: string) =>
    feedback?.scope === scope ? <ActionNote tone={feedback.tone}>{feedback.message}</ActionNote> : null

  const video = detail?.video
  const sidecars = detail?.sidecars ?? []
  const wantedRows = detail?.wanted ?? []
  const missingRows = wantedRows.filter((row) => row.status === 'wanted')
  const outputs = detail?.outputs ?? []
  const subtitleStreams = streams.filter((stream) => stream.type === 'subtitle')
  const audioStreams = streams.filter((stream) => stream.type === 'audio')

  // waitJob follows a queued job until it reaches a terminal state.
  const waitJob = useCallback(
    async (created: SubtitleJob, label: string, scope: Task) => {
      setJob({ job: created, task: scope })
      let failure = ''
      let outcome = 'done'
      for (let attempt = 0; attempt < 200 && !cancelled.current; attempt++) {
        await new Promise((resolve) => window.setTimeout(resolve, 1200))
        try {
          const current = await subtitlesApi.job(created.id)
          setJob({ job: current, task: scope })
          if (current.status === 'queued' || current.status === 'running') continue
          if (current.status === 'cancelled') outcome = 'cancelled'
          else if (current.status !== 'done') {
            outcome = 'failed'
            failure = current.error || current.detail || current.status
          }
          break
        } catch (cause) {
          outcome = 'failed'
          failure = errorMessage(cause)
          break
        }
      }
      const before = outputsRef.current
      const next = await reload().catch(() => undefined)
      setJob(null)
      if (outcome === 'failed') {
        setFeedback({ scope, tone: 'error', message: `${label} failed: ${failure}` })
      } else if (outcome === 'cancelled') {
        setFeedback({ scope, tone: 'success', message: `${label} cancelled.` })
      } else if (next && next.outputs.length > before) {
        setFeedback({
          scope,
          tone: 'success',
          message: `${label} finished. The result is waiting for review under Subtitles — preview it and save it when it looks right.`,
        })
      } else {
        setFeedback({ scope, tone: 'success', message: `${label} finished.` })
      }
      onChanged()
    },
    [onChanged, reload],
  )

  const runSearch = async () => {
    if (!target || !detail) return
    setSearching(true)
    setFeedback(null)
    setWarnings([])
    try {
      const languages = missingRows
        .map((row) => ({ code: row.language, forced: row.forced, hi: row.hi }))
      const outcome = await subtitlesApi.search(target.kind, target.id, languages)
      setResults(outcome.results)
      setWarnings(outcome.warnings)
    } catch (cause) {
      setFeedback({ scope: 'find', tone: 'error', message: errorMessage(cause) })
    } finally {
      setSearching(false)
    }
  }

  const download = async (result: SubtitleResult) => {
    if (!target) return
    setFeedback(null)
    try {
      const created = await subtitlesApi.download(target.kind, target.id, {
        providerId: result.providerId,
        fileId: result.fileId,
        language: result.language,
        forced: result.forced,
        hi: result.hi,
        fileName: result.fileName,
      })
      await waitJob(created, 'Download', 'find')
    } catch (cause) {
      setFeedback({ scope: 'find', tone: 'error', message: errorMessage(cause) })
    }
  }

  const runSync = async () => {
    if (!target) return
    setFeedback(null)
    try {
      const created = await subtitlesApi.sync(target.kind, target.id, {
        path: syncPath,
        mode: syncMode,
        offsetSeconds: Number(offset) || 0,
        fpsFrom: Number(fpsFrom) || 0,
        fpsTo: Number(fpsTo) || 0,
        referencePath,
        audioStream: Number(audioStream),
        maxOffsetSeconds: maxOffset ? Number(maxOffset) : undefined,
        minScore: minScore ? Number(minScore) : undefined,
        noFixFramerate,
        goldenSectionSearch: goldenSection,
        preview: syncPreview,
      })
      await waitJob(created, 'Timing adjustment', 'timing')
    } catch (cause) {
      setFeedback({ scope: 'timing', tone: 'error', message: errorMessage(cause) })
    }
  }

  const runTranslate = async () => {
    if (!target) return
    setFeedback(null)
    try {
      const created = await subtitlesApi.translate(target.kind, target.id, {
        path: translatePath,
        language: translateLanguage,
      })
      await waitJob(created, 'Translation', 'translate')
    } catch (cause) {
      setFeedback({ scope: 'translate', tone: 'error', message: errorMessage(cause) })
    }
  }

  const runExtract = async () => {
    if (!target) return
    setFeedback(null)
    try {
      const created = await subtitlesApi.extract(target.kind, target.id, {
        streamIndex: Number(extractStream),
        language: extractLanguage,
        preview: extractPreview,
      })
      await waitJob(created, 'Extraction', 'extract')
    } catch (cause) {
      setFeedback({ scope: 'extract', tone: 'error', message: errorMessage(cause) })
    }
  }

  const cancelJob = async (id: string) => {
    try {
      await subtitlesApi.cancelJob(id)
    } catch (cause) {
      setFeedback({ scope: job?.task ?? 'subtitles', tone: 'error', message: errorMessage(cause) })
    }
  }

  const applyOutput = async (output: SubtitleOutput) => {
    const scope = `output:${output.id}`
    setFeedback(null)
    try {
      const sidecar = await subtitlesApi.applyOutput(output.id)
      setFeedback({ scope, tone: 'success', message: `Saved as ${sidecar.path.split('/').pop()}.` })
      await reload()
      onChanged()
    } catch (cause) {
      setFeedback({ scope, tone: 'error', message: errorMessage(cause) })
    }
  }

  const discardOutput = async (output: SubtitleOutput) => {
    const scope = `output:${output.id}`
    setFeedback(null)
    try {
      await subtitlesApi.discardOutput(output.id)
      setFeedback({ scope, tone: 'success', message: 'Discarded the staged subtitle.' })
      await reload()
      onChanged()
    } catch (cause) {
      setFeedback({ scope, tone: 'error', message: errorMessage(cause) })
    }
  }

  const saveAssignment = async () => {
    if (!target) return
    setFeedback(null)
    try {
      await subtitlesApi.setAssignment(target.kind, target.id, profileID, monitored)
      setFeedback({
        scope: 'profile',
        tone: 'success',
        message: `Saved. Looking for ${profileName || 'the selected profile'} languages.`,
      })
      await reload()
      onChanged()
    } catch (cause) {
      setFeedback({ scope: 'profile', tone: 'error', message: errorMessage(cause) })
    }
  }

  const profileName = profiles.find((item) => item.id === profileID)?.name ?? ''
  const writeReasonId = 'subtitle-dialog-write-reason'

  const searchReason = !canWrite
    ? 'Your role can review subtitles but not search for them.'
    : missingRows.length === 0
      ? 'This video has no missing languages to look for. Choose a language profile with at least one language first.'
      : ''
  const syncReason = !canWrite
    ? 'Your role can review subtitles but not change them.'
    : sidecars.length === 0
      ? 'No subtitle file to adjust yet. Find or extract one first.'
      : !syncPath
        ? 'Choose a subtitle file first.'
        : syncMode === 'reference' && !referencePath
          ? 'Choose the subtitle file that already matches the video.'
          : ''
  const translateReason = !canWrite
    ? 'Your role can review subtitles but not change them.'
    : sidecars.length === 0
      ? 'No subtitle file to translate yet. Find or extract one first.'
      : !translatePath
        ? 'Choose a subtitle file first.'
        : !translateLanguage
          ? 'Choose the language to translate into.'
          : ''
  const extractReason = !canWrite
    ? 'Your role can review subtitles but not change them.'
    : subtitleStreams.length === 0
      ? 'This video has no embedded subtitle tracks to extract.'
      : Number(extractStream) < 0
        ? 'Choose an embedded track first.'
        : ''

  return (
    <DialogShell
      active={target !== null}
      title={video ? videoTitle(video) : 'Subtitles'}
      description={
        video
          ? [
              videoMeta(video),
              sidecars.length === 0 ? 'no subtitle files yet' : `${sidecars.length} subtitle ${sidecars.length === 1 ? 'file' : 'files'}`,
              outputs.length > 0 ? `${outputs.length} waiting for review` : '',
            ]
              .filter(Boolean)
              .join(' · ')
          : 'Loading video…'
      }
      onClose={onClose}
    >
      {loading && !detail ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" /> Loading subtitles…
        </p>
      ) : (
        <>
          {error && <ErrorNote onRetry={() => reload().catch((cause) => setError(errorMessage(cause)))}>{error}</ErrorNote>}
          {!canWrite && (
            <p id={writeReasonId} className="rounded-md border border-border px-3 py-2 text-xs text-muted-foreground">
              You can review subtitles here. Searching, downloading, adjusting timing, translating, and extracting need
              the subtitles write permission.
            </p>
          )}

          <TabButtons
            label="Subtitle tasks"
            value={task}
            onChange={setTask}
            items={tasks.map((item) => ({
              value: item.value,
              label: item.label,
              icon: item.icon,
              count: item.value === 'subtitles' ? sidecars.length : item.value === 'find' ? results.length : 0,
            }))}
          />

          {task === 'subtitles' && (
            <div className="space-y-5">
              <p className="text-sm text-muted-foreground">
                Subtitle files for this video, and what Constellarr is still looking for.
              </p>

              <Section title={`Subtitle files (${sidecars.length})`}>
                {sidecars.length === 0 ? (
                  <EmptyNote>
                    <p>No subtitle files for this video yet.</p>
                    <div className="mt-3 flex flex-wrap gap-2">
                      <Button size="sm" variant="outline" onClick={() => setTask('find')}>
                        <ScanSearchIcon data-icon="inline-start" />
                        Find subtitles
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => setTask('extract')}>
                        <FileDownIcon data-icon="inline-start" />
                        Extract from video
                      </Button>
                    </div>
                  </EmptyNote>
                ) : (
                  <ul className="divide-y divide-border rounded-lg border border-border">
                    {sidecars.map((sidecar) => (
                      <li key={sidecar.path} className="space-y-2 p-2.5">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <span className="flex flex-wrap items-center gap-2 text-sm">
                            <VariantBadges item={sidecar} />
                            <span className="text-xs text-muted-foreground">
                              {sidecar.format.toUpperCase()} · {formatBytes(sidecar.size)} · {sourceLabel(sidecar.source)} ·{' '}
                              {timeLabel(sidecar.updatedAt)}
                            </span>
                          </span>
                          <span className="flex flex-wrap gap-1">
                            <Button asChild size="xs" variant="ghost">
                              <a href={subtitlesApi.fileUrl(sidecar.kind, sidecar.videoId, sidecar.path)} download>
                                <FileDownIcon data-icon="inline-start" />
                                Download file
                              </a>
                            </Button>
                            <Button
                              size="xs"
                              variant="ghost"
                              onClick={() => {
                                setSyncPath(sidecar.path)
                                setTask('timing')
                              }}
                            >
                              Adjust timing
                            </Button>
                            <Button
                              size="xs"
                              variant="ghost"
                              onClick={() => {
                                setTranslatePath(sidecar.path)
                                setTask('translate')
                              }}
                            >
                              Translate
                            </Button>
                          </span>
                        </div>
                        <Disclosure variant="inline" label="File details">
                          <p className="break-all">{sidecar.path}</p>
                        </Disclosure>
                      </li>
                    ))}
                  </ul>
                )}
              </Section>

              <Section title={`Still looking for (${missingRows.length})`}>
                {missingRows.length === 0 ? (
                  <EmptyNote>Nothing is missing. Every language in the profile for this video is covered.</EmptyNote>
                ) : (
                  <ul className="space-y-2 rounded-lg border border-border p-3">
                    {missingRows.map((row) => (
                      <li key={`${row.language}-${row.forced}-${row.hi}`} className="flex flex-wrap items-center gap-2 text-sm">
                        <VariantBadges item={row} />
                        <span className="text-xs text-muted-foreground">
                          {row.nextAttemptAt ? `next try ${timeLabel(row.nextAttemptAt)}` : 'waiting for the next search'}
                          {row.attempts > 0 ? ` · ${row.attempts} ${row.attempts === 1 ? 'try' : 'tries'}` : ''}
                        </span>
                        {row.error && <ActionNote tone="error">{row.error}</ActionNote>}
                      </li>
                    ))}
                  </ul>
                )}
              </Section>

              <Section title={`Waiting for review (${outputs.length})`}>
                {outputs.length === 0 ? (
                  <EmptyNote>
                    Nothing is waiting for review. Timing, translation, and extraction results appear here so you can
                    check them before they replace anything.
                  </EmptyNote>
                ) : (
                  <ul className="divide-y divide-border rounded-lg border border-border">
                    {outputs.map((output) => (
                      <li key={output.id} className="space-y-2 p-2.5">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <div className="flex flex-wrap items-center gap-2 text-sm">
                            <VariantBadges item={output} />
                            <Badge variant="outline">{output.format.toUpperCase()}</Badge>
                            <span className="text-xs text-muted-foreground">{output.detail || 'prepared, not saved yet'}</span>
                          </div>
                          <div className="flex gap-1">
                            <Button size="xs" variant="ghost" onClick={() => setPreviewID(previewID === output.id ? '' : output.id)}>
                              {previewID === output.id ? 'Hide' : 'Preview'}
                            </Button>
                            <Button
                              size="xs"
                              variant="secondary"
                              onClick={() => applyOutput(output)}
                              disabled={!canWrite}
                              aria-describedby={canWrite ? undefined : writeReasonId}
                            >
                              <SaveIcon data-icon="inline-start" />
                              Save
                            </Button>
                            <Button
                              size="xs"
                              variant="destructive"
                              onClick={() => discardOutput(output)}
                              disabled={!canWrite}
                              aria-describedby={canWrite ? undefined : writeReasonId}
                            >
                              <BanIcon data-icon="inline-start" />
                              Discard
                            </Button>
                          </div>
                        </div>
                        {noteFor(`output:${output.id}`)}
                        {previewID === output.id && (
                          <pre className="max-h-56 overflow-auto rounded-md border border-border bg-muted/40 p-2 text-xs whitespace-pre-wrap">
                            {summarise(output.payload ?? '')}
                          </pre>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </Section>

              <Section
                title="Language profile"
                action={
                  canWrite && (
                    <Button size="xs" variant="outline" onClick={saveAssignment} disabled={!profileID}>
                      <SaveIcon data-icon="inline-start" />
                      Save
                    </Button>
                  )
                }
              >
                <div className="grid gap-2 sm:grid-cols-2">
                  <Field label="Which languages to look for">
                    <Select
                      value={profileID}
                      onChange={(event) => setProfileID(event.target.value)}
                      disabled={!canWrite || profiles.length === 0}
                      aria-describedby={canWrite ? undefined : writeReasonId}
                    >
                      {profiles.length === 0 && <option value="">No profiles yet</option>}
                      {profiles.map((profile) => (
                        <option key={profile.id} value={profile.id}>
                          {profile.name}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <div className="flex items-end pb-2">
                    <Toggle
                      label="Search for this video automatically"
                      hint="Turn this off to leave this video alone."
                      checked={monitored}
                      onChange={setMonitored}
                      disabled={!canWrite}
                    />
                  </div>
                </div>
                {profiles.length === 0 && canSettingsRead && (
                  <ActionNote>No language profiles exist yet. Create one under Settings → Languages.</ActionNote>
                )}
                {noteFor('profile')}
              </Section>

              <Section
                title="Recent activity"
                action={
                  <Button size="xs" variant="ghost" onClick={() => reload().catch((cause) => setError(errorMessage(cause)))}>
                    <RefreshCwIcon data-icon="inline-start" />
                    Refresh
                  </Button>
                }
              >
                {(detail?.history ?? []).length === 0 ? (
                  <EmptyNote>Nothing has happened with subtitles for this video yet.</EmptyNote>
                ) : (
                  <ul className="divide-y divide-border rounded-lg border border-border">
                    {(detail?.history ?? []).map((entry) => (
                      <li key={entry.id} className="flex items-start gap-2 p-2.5 text-sm">
                        <Badge variant={statusVariant(entry.action)}>{entry.action}</Badge>
                        <span className="min-w-0 flex-1">
                          <span className="break-words">{entry.message}</span>
                          <span className="block text-xs text-muted-foreground">{timeLabel(entry.createdAt)}</span>
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </Section>
            </div>
          )}

          {task === 'find' && (
            <div className="space-y-5">
              <p className="text-sm text-muted-foreground">
                Search your providers for the languages this video still needs. Nothing is downloaded until you choose a
                result.
              </p>
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <span className="text-muted-foreground">Looking for:</span>
                {missingRows.length === 0 ? (
                  <span className="text-muted-foreground">no missing languages</span>
                ) : (
                  missingRows.map((row) => <VariantBadges key={`${row.language}-${row.forced}-${row.hi}`} item={row} />)
                )}
              </div>
              <div className="space-y-2">
                <Button
                  onClick={runSearch}
                  disabled={!canWrite || missingRows.length === 0 || searching}
                  aria-describedby={searchReason ? 'subtitle-search-reason' : undefined}
                >
                  {searching ? (
                    <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                  ) : (
                    <ScanSearchIcon data-icon="inline-start" />
                  )}
                  {searching ? 'Searching…' : 'Find subtitles'}
                </Button>
                {searchReason && <ActionNote id="subtitle-search-reason">{searchReason}</ActionNote>}
                {missingRows.length === 0 && canSettingsRead && (
                  <Button size="sm" variant="outline" onClick={() => setTask('subtitles')}>
                    Choose a language profile
                  </Button>
                )}
                {noteFor('find')}
                {job?.task === 'find' && <JobProgress job={job.job} canWrite={canWrite} onCancel={() => cancelJob(job.job.id)} />}
              </div>
              {providerReady === false && (
                <ActionNote tone="warning">
                  No subtitle provider is connected yet, so a search returns nothing. Add an OpenSubtitles API key under
                  Settings first.
                </ActionNote>
              )}
              {warnings.length > 0 && (
                <ul className="space-y-1">
                  {warnings.map((warning) => (
                    <li key={warning}>
                      <ActionNote tone="warning">{warning}</ActionNote>
                    </li>
                  ))}
                </ul>
              )}
              <Section title={`Results (${results.length})`}>
                {results.length === 0 ? (
                  <EmptyNote>
                    {searching
                      ? 'Searching your providers…'
                      : warnings.length > 0
                        ? 'No results came back. Check the messages above and try again.'
                        : 'No results yet. Use Find subtitles to search your providers.'}
                  </EmptyNote>
                ) : (
                  <ul className="divide-y divide-border rounded-lg border border-border">
                    {results.map((result) => (
                      <li key={`${result.providerId}-${result.fileId}`} className="space-y-2 p-2.5">
                        <div className="flex flex-wrap items-start justify-between gap-2">
                          <div className="min-w-0 space-y-1">
                            <p className="break-words text-sm font-medium">
                              {result.release || result.fileName || `Subtitle from ${result.providerName}`}
                            </p>
                            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                              <VariantBadges item={result} />
                              <span>{(result.format || 'srt').toUpperCase()}</span>
                              {result.fps > 0 && <span>{result.fps} fps</span>}
                              <span>{result.downloads.toLocaleString()} downloads</span>
                              <span>{result.providerName}</span>
                            </div>
                          </div>
                          <Button
                            size="xs"
                            onClick={() => download(result)}
                            disabled={!canWrite}
                            aria-describedby={canWrite ? undefined : writeReasonId}
                          >
                            <DownloadIcon data-icon="inline-start" />
                            Download
                          </Button>
                        </div>
                        <Disclosure variant="inline" label="File details">
                          <p className="break-all">{result.fileName || 'no file name'}</p>
                          <p>
                            Match score {result.score}
                            {result.matchedBy ? ` · matched by ${result.matchedBy}` : ''}
                            {result.rating > 0 ? ` · rating ${result.rating}` : ''} · file {result.fileId}
                          </p>
                        </Disclosure>
                      </li>
                    ))}
                  </ul>
                )}
              </Section>
            </div>
          )}

          {task === 'timing' && (
            <div className="space-y-5">
              <p className="text-sm text-muted-foreground">
                Line a subtitle file up with this video so the text matches what you hear.
              </p>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Subtitle file">
                  <Select value={syncPath} onChange={(event) => setSyncPath(event.target.value)} disabled={sidecars.length === 0}>
                    <option value="">{sidecars.length === 0 ? 'No subtitle files yet' : 'Choose a subtitle file…'}</option>
                    {sidecars.map((sidecar) => (
                      <option key={sidecar.path} value={sidecar.path}>
                        {sidecarLabel(sidecar)}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field label="What to adjust" hint="Offset and frame rate are instant. Audio and another subtitle use the bundled ffsubsync helper.">
                  <Select value={syncMode} onChange={(event) => setSyncMode(event.target.value as typeof syncMode)}>
                    {syncModes.map((mode) => (
                      <option key={mode.value} value={mode.value}>
                        {mode.label}
                      </option>
                    ))}
                  </Select>
                </Field>
                {syncMode === 'offset' && (
                  <Field label="Offset in seconds" hint="Positive starts the subtitles later, negative earlier.">
                    <Input value={offset} onChange={(event) => setOffset(event.target.value)} inputMode="decimal" />
                  </Field>
                )}
                {syncMode === 'fps' && (
                  <>
                    <Field label="Subtitle frame rate">
                      <Input value={fpsFrom} onChange={(event) => setFpsFrom(event.target.value)} inputMode="decimal" />
                    </Field>
                    <Field label="Video frame rate">
                      <Input value={fpsTo} onChange={(event) => setFpsTo(event.target.value)} inputMode="decimal" />
                    </Field>
                  </>
                )}
                {syncMode === 'reference' && (
                  <Field label="Reference subtitle" hint="The file that already matches the video.">
                    <Select value={referencePath} onChange={(event) => setReferencePath(event.target.value)} disabled={sidecars.length < 2}>
                      <option value="">{sidecars.length < 2 ? 'Need two subtitle files' : 'Choose a reference…'}</option>
                      {sidecars.map((sidecar) => (
                        <option key={sidecar.path} value={sidecar.path}>
                          {sidecarLabel(sidecar)}
                        </option>
                      ))}
                    </Select>
                  </Field>
                )}
                {syncMode === 'audio' && (
                  <Field label="Audio track" hint={streamsError || 'Automatic uses the first audio track.'}>
                    <Select value={audioStream} onChange={(event) => setAudioStream(event.target.value)}>
                      <option value="-1">Automatic</option>
                      {audioStreams.map((stream) => (
                        <option key={stream.index} value={stream.index}>
                          {audioStreamLabel(stream)}
                        </option>
                      ))}
                    </Select>
                  </Field>
                )}
              </div>
              <Toggle
                label="Review before saving"
                hint="Keep the current file and stage the adjusted copy until you approve it."
                checked={syncPreview}
                onChange={setSyncPreview}
              />
              <div className="space-y-2">
                <Button
                  onClick={runSync}
                  disabled={Boolean(syncReason)}
                  aria-describedby={syncReason ? 'subtitle-sync-reason' : undefined}
                >
                  <PlayIcon data-icon="inline-start" />
                  Adjust timing
                </Button>
                {syncReason && <ActionNote id="subtitle-sync-reason">{syncReason}</ActionNote>}
                {noteFor('timing')}
                {job?.task === 'timing' && <JobProgress job={job.job} canWrite={canWrite} onCancel={() => cancelJob(job.job.id)} />}
              </div>
              <Disclosure label="Advanced options">
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Maximum offset in seconds" hint="Leave blank to use the configured limit.">
                    <Input value={maxOffset} onChange={(event) => setMaxOffset(event.target.value)} inputMode="decimal" />
                  </Field>
                  <Field label="Minimum alignment score" hint="0 rejects only clearly wrong alignments.">
                    <Input value={minScore} onChange={(event) => setMinScore(event.target.value)} inputMode="decimal" />
                  </Field>
                </div>
                {syncMode !== 'offset' && syncMode !== 'fps' && (
                  <div className="grid gap-2 sm:grid-cols-2">
                    <Toggle
                      label="Search frame rates step by step"
                      hint="Slower, but handles unusual frame rate ratios."
                      checked={goldenSection}
                      onChange={setGoldenSection}
                    />
                    <Toggle
                      label="Keep the subtitle frame rate"
                      hint="Only looks for an offset, never changes the frame rate."
                      checked={noFixFramerate}
                      onChange={setNoFixFramerate}
                    />
                  </div>
                )}
              </Disclosure>
            </div>
          )}

          {task === 'translate' && (
            <div className="space-y-5">
              <p className="text-sm text-muted-foreground">
                Create a subtitle file in another language. Every line is checked and the result waits for review before it
                is saved.
              </p>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Subtitle file to translate">
                  <Select value={translatePath} onChange={(event) => setTranslatePath(event.target.value)} disabled={sidecars.length === 0}>
                    <option value="">{sidecars.length === 0 ? 'No subtitle files yet' : 'Choose a subtitle file…'}</option>
                    {sidecars.map((sidecar) => (
                      <option key={sidecar.path} value={sidecar.path}>
                        {sidecarLabel(sidecar)}
                      </option>
                    ))}
                  </Select>
                </Field>
                <Field
                  label="Translate into"
                  hint={translateLanguage ? `Language code ${translateLanguage}` : 'Pick the language you want to read.'}
                >
                  <Select value={translateLanguage} onChange={(event) => setTranslateLanguage(event.target.value)}>
                    <option value="">Choose a language…</option>
                    {languageOptions([
                      ...wantedRows.map((row) => row.language),
                      ...sidecars.map((sidecar) => sidecar.language),
                      translateLanguage,
                    ]).map((option) => (
                      <option key={option.code} value={option.code}>
                        {option.name}
                      </option>
                    ))}
                  </Select>
                </Field>
              </div>
              <div className="space-y-2">
                <Button onClick={runTranslate} disabled={Boolean(translateReason)} aria-describedby={translateReason ? 'subtitle-translate-reason' : undefined}>
                  <LanguagesIcon data-icon="inline-start" />
                  Translate
                </Button>
                {translateReason && <ActionNote id="subtitle-translate-reason">{translateReason}</ActionNote>}
                {noteFor('translate')}
                {job?.task === 'translate' && <JobProgress job={job.job} canWrite={canWrite} onCancel={() => cancelJob(job.job.id)} />}
              </div>
              <ActionNote>
                Translation uses the OpenAI-compatible service configured once under Connections. Results are staged for
                review, so nothing is overwritten automatically.
              </ActionNote>
            </div>
          )}

          {task === 'extract' && (
            <div className="space-y-5">
              <p className="text-sm text-muted-foreground">
                Pull out a subtitle track that is stored inside the video file itself.
              </p>
              {streamsLoading ? (
                <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
                  <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" /> Reading embedded tracks…
                </p>
              ) : subtitleStreams.length === 0 ? (
                <EmptyNote>
                  {streamsError
                    ? `Embedded tracks could not be read: ${streamsError}`
                    : 'This video has no embedded subtitle tracks, so there is nothing to extract.'}
                </EmptyNote>
              ) : (
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Embedded subtitle track">
                    <Select value={extractStream} onChange={(event) => setExtractStream(event.target.value)}>
                      <option value="-1">Choose a track…</option>
                      {subtitleStreams.map((stream) => (
                        <option key={stream.index} value={stream.index}>
                          {subtitleStreamLabel(stream)}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <Field label="Language override" hint="Only needed when the track has no language tag.">
                    <Input value={extractLanguage} onChange={(event) => setExtractLanguage(event.target.value)} placeholder="en" />
                  </Field>
                </div>
              )}
              {subtitleStreams.length > 0 && (
                <>
                  <Toggle
                    label="Review before saving"
                    hint="Stage the extracted subtitles until you approve them."
                    checked={extractPreview}
                    onChange={setExtractPreview}
                  />
                  <div className="space-y-2">
                    <Button onClick={runExtract} disabled={Boolean(extractReason)} aria-describedby={extractReason ? 'subtitle-extract-reason' : undefined}>
                      <DownloadIcon data-icon="inline-start" />
                      Extract subtitle track
                    </Button>
                    {extractReason && <ActionNote id="subtitle-extract-reason">{extractReason}</ActionNote>}
                    {noteFor('extract')}
                    {job?.task === 'extract' && <JobProgress job={job.job} canWrite={canWrite} onCancel={() => cancelJob(job.job.id)} />}
                  </div>
                  <Disclosure label="Track details" detail={`${streams.length} tracks in the file`}>
                    <ul className="space-y-1">
                      {[...subtitleStreams, ...audioStreams].map((stream) => (
                        <li key={`${stream.type}-${stream.index}`}>
                          #{stream.index} · {stream.type} · {stream.codec}
                          {stream.language ? ` · ${languageName(stream.language)}` : ' · no language tag'}
                          {stream.title ? ` · ${stream.title}` : ''}
                          {stream.default ? ' · default' : ''}
                        </li>
                      ))}
                    </ul>
                  </Disclosure>
                </>
              )}
            </div>
          )}
        </>
      )}
    </DialogShell>
  )
}
