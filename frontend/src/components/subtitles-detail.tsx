import { useCallback, useEffect, useRef, useState } from 'react'
import {
  BanIcon,
  DownloadIcon,
  FileDownIcon,
  LanguagesIcon,
  LoaderCircleIcon,
  PlayIcon,
  RefreshCwIcon,
  ScanSearchIcon,
  SaveIcon,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DialogShell, EmptyNote, ErrorNote, Field, Notice, Section, Select, Toggle, VariantBadges } from '@/components/subtitles-ui'
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
  type SubtitleStream,
} from '@/lib/subtitles-api'

type Target = { kind: string; id: string; mode: 'detail' | 'search' } | null

function summarise(text: string, lines = 12) {
  const parts = text.split('\n')
  return parts.slice(0, lines).join('\n') + (parts.length > lines ? '\n…' : '')
}

export function SubtitleDetailDialog({
  target,
  profiles,
  canWrite,
  onClose,
  onChanged,
}: {
  target: Target
  profiles: SubtitleProfile[]
  canWrite: boolean
  onClose: () => void
  onChanged: () => void
}) {
  const [detail, setDetail] = useState<SubtitleDetail | null>(null)
  const [streams, setStreams] = useState<SubtitleStream[]>([])
  const [streamsError, setStreamsError] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [job, setJob] = useState<SubtitleJob | null>(null)
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
  const [translateLanguage, setTranslateLanguage] = useState('de')
  const [extractStream, setExtractStream] = useState('-1')
  const [extractLanguage, setExtractLanguage] = useState('')
  const [extractPreview, setExtractPreview] = useState(false)

  const cancelled = useRef(false)
  useEffect(() => {
    cancelled.current = false
    return () => {
      cancelled.current = true
    }
  }, [])

  const reload = useCallback(async () => {
    if (!target) return
    setDetail(await subtitlesApi.detail(target.kind, target.id))
  }, [target])

  const load = useCallback(
    async (active: { kind: string; id: string }, signal?: AbortSignal) => {
      try {
        setLoading(true)
        setNotice('')
        setJob(null)
        setResults([])
        setWarnings([])
        setPreviewID('')
        const loaded = await subtitlesApi.detail(active.kind, active.id, signal)
        if (signal?.aborted) return
        setDetail(loaded)
        setProfileID(loaded.profileId)
        setMonitored(loaded.monitored)
        const first = loaded.sidecars[0]?.path ?? ''
        setSyncPath(first)
        setTranslatePath(first)
        setReferencePath(loaded.sidecars.find((item) => item.path !== first)?.path ?? '')
        setError('')
      } catch (cause) {
        if (!signal?.aborted) setError(errorMessage(cause))
      } finally {
        if (!signal?.aborted) setLoading(false)
      }
      try {
        const list = await subtitlesApi.streams(active.kind, active.id, signal)
        if (!signal?.aborted) {
          setStreams(list)
          setStreamsError('')
        }
      } catch (cause) {
        if (!signal?.aborted) setStreamsError(errorMessage(cause))
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

  // waitJob follows a queued job until it reaches a terminal state.
  const waitJob = useCallback(
    async (created: SubtitleJob, label: string) => {
      setJob(created)
      for (let attempt = 0; attempt < 200 && !cancelled.current; attempt++) {
        await new Promise((resolve) => window.setTimeout(resolve, 1200))
        try {
          const current = await subtitlesApi.job(created.id)
          setJob(current)
          if (current.status !== 'queued' && current.status !== 'running') {
            if (current.status === 'done') setNotice(`${label}: ${current.detail || 'done'}`)
            else setError(`${label} failed: ${current.error || current.detail || current.status}`)
            break
          }
        } catch (cause) {
          setError(errorMessage(cause))
          break
        }
      }
      await reload().catch(() => undefined)
      onChanged()
    },
    [onChanged, reload],
  )

  const runSearch = async () => {
    if (!target || !detail) return
    setSearching(true)
    setError('')
    setWarnings([])
    try {
      const languages = (detail.wanted ?? [])
        .filter((row) => row.status === 'wanted')
        .map((row) => ({ code: row.language, forced: row.forced, hi: row.hi }))
      const outcome = await subtitlesApi.search(target.kind, target.id, languages)
      setResults(outcome.results)
      setWarnings(outcome.warnings)
      if (outcome.results.length === 0) setNotice('No provider result matched the wanted languages.')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setSearching(false)
    }
  }

  const download = async (result: SubtitleResult) => {
    if (!target) return
    setError('')
    try {
      const created = await subtitlesApi.download(target.kind, target.id, {
        providerId: result.providerId,
        fileId: result.fileId,
        language: result.language,
        forced: result.forced,
        hi: result.hi,
        fileName: result.fileName,
      })
      await waitJob(created, `Download ${result.fileName || result.language}`)
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const runSync = async () => {
    if (!target) return
    setError('')
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
      await waitJob(created, 'Synchronization')
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const runTranslate = async () => {
    if (!target) return
    setError('')
    try {
      const created = await subtitlesApi.translate(target.kind, target.id, {
        path: translatePath,
        language: translateLanguage,
      })
      await waitJob(created, 'Translation')
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const runExtract = async () => {
    if (!target) return
    setError('')
    try {
      const created = await subtitlesApi.extract(target.kind, target.id, {
        streamIndex: Number(extractStream),
        language: extractLanguage,
        preview: extractPreview,
      })
      await waitJob(created, 'Extraction')
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const applyOutput = async (output: SubtitleOutput) => {
    setError('')
    try {
      const sidecar = await subtitlesApi.applyOutput(output.id)
      setNotice(`Saved ${sidecar.path}`)
      await reload()
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const discardOutput = async (output: SubtitleOutput) => {
    setError('')
    try {
      await subtitlesApi.discardOutput(output.id)
      setNotice('Discarded the staged subtitle.')
      await reload()
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const saveAssignment = async () => {
    if (!target) return
    try {
      await subtitlesApi.setAssignment(target.kind, target.id, profileID, monitored)
      setNotice('Language profile updated.')
      await reload()
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const video = detail?.video
  const sidecars = detail?.sidecars ?? []
  const wanted = detail?.wanted ?? []
  const outputs = detail?.outputs ?? []
  const subtitleStreams = streams.filter((stream) => stream.type === 'subtitle')
  const audioStreams = streams.filter((stream) => stream.type === 'audio')

  return (
    <DialogShell
      active={target !== null}
      title={video ? video.seriesTitle ? `${video.seriesTitle} — ${video.title}` : video.title : 'Subtitle detail'}
      description={video ? video.path : 'Loading video…'}
      onClose={onClose}
    >
      {loading && !detail && (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" /> Loading subtitle state…
        </p>
      )}
      {error && <ErrorNote>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}
      {!canWrite && (
        <p className="rounded-md border border-border px-3 py-2 text-xs text-muted-foreground">
          Your role can review subtitle state; changes require the subtitles write permission.
        </p>
      )}
      {job && (
        <p role="status" className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <Badge variant={statusVariant(job.status)}>{job.status}</Badge>
          <span className="min-w-0 break-words">{job.detail || job.error || `${job.progress}%`}</span>
        </p>
      )}

      <Section
        title="Language profile"
        action={
          canWrite &&
          profiles.length > 0 && (
            <Button size="xs" variant="outline" onClick={saveAssignment}>
              <SaveIcon data-icon="inline-start" />
              Save
            </Button>
          )
        }
      >
        <div className="grid gap-2 sm:grid-cols-2">
          <Field label="Profile">
            <Select value={profileID} onChange={(event) => setProfileID(event.target.value)}>
              {profiles.map((profile) => (
                <option key={profile.id} value={profile.id}>
                  {profile.name}
                </option>
              ))}
            </Select>
          </Field>
          <div className="flex items-end pb-2">
            <Toggle label="Monitor this video" checked={monitored} onChange={setMonitored} disabled={!canWrite} />
          </div>
        </div>
      </Section>

      <Section
        title={`Sidecar files (${sidecars.length})`}
        action={
          canWrite && (
          <Button size="xs" variant="outline" onClick={runSearch} disabled={searching}>
            {searching ? (
              <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
            ) : (
              <ScanSearchIcon data-icon="inline-start" />
            )}
            Search providers
          </Button>
          )
        }
      >
        {sidecars.length === 0 ? (
          <EmptyNote>No subtitle sidecars yet. Search a provider or extract an embedded track.</EmptyNote>
        ) : (
          <ul className="divide-y divide-border rounded-lg border border-border">
            {sidecars.map((sidecar) => (
              <li key={sidecar.path} className="flex flex-wrap items-center justify-between gap-2 p-2.5">
                <div className="min-w-0 space-y-1">
                  <p className="truncate text-sm font-medium">{sidecar.path.split('/').pop()}</p>
                  <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <VariantBadges item={sidecar} />
                    <span>{sidecar.format.toUpperCase()}</span>
                    <span>{formatBytes(sidecar.size)}</span>
                    <span>{sidecar.source}</span>
                    <span>{timeLabel(sidecar.updatedAt)}</span>
                  </div>
                </div>
                <div className="flex gap-1">
                  <Button asChild size="xs" variant="ghost">
                    <a href={subtitlesApi.fileUrl(sidecar.kind, sidecar.videoId, sidecar.path)} download>
                      <FileDownIcon data-icon="inline-start" />
                      File
                    </a>
                  </Button>
                  {canWrite && (
                    <>
                      <Button size="xs" variant="ghost" onClick={() => setSyncPath(sidecar.path)}>
                        Use for sync
                      </Button>
                      <Button size="xs" variant="ghost" onClick={() => setTranslatePath(sidecar.path)}>
                        Use for translation
                      </Button>
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
        {wanted.length > 0 && (
          <ul className="space-y-1 text-xs text-muted-foreground">
            {wanted.map((row) => (
              <li key={`${row.language}-${row.forced}-${row.hi}`} className="flex flex-wrap items-center gap-2">
                <Badge variant={statusVariant(row.status)}>{row.status}</Badge>
                <VariantBadges item={row} />
                {row.error && <span className="text-destructive">{row.error}</span>}
                {row.status === 'wanted' && row.nextAttemptAt && <span>retry {timeLabel(row.nextAttemptAt)}</span>}
              </li>
            ))}
          </ul>
        )}
      </Section>

      {(results.length > 0 || warnings.length > 0) && (
        <Section title={`Provider results (${results.length})`}>
          {warnings.map((warning) => (
            <p key={warning} className="break-words text-xs text-amber-500">
              {warning}
            </p>
          ))}
          <ul className="divide-y divide-border rounded-lg border border-border">
            {results.map((result) => (
              <li key={`${result.providerId}-${result.fileId}`} className="flex flex-wrap items-center justify-between gap-2 p-2.5">
                <div className="min-w-0 space-y-1">
                  <p className="truncate text-sm font-medium">{result.release || result.fileName || `File ${result.fileId}`}</p>
                  <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <VariantBadges item={result} />
                    <span>{(result.format || 'srt').toUpperCase()}</span>
                    {result.fps > 0 && <span>{result.fps} fps</span>}
                    <span>{result.downloads.toLocaleString()} downloads</span>
                    <span>score {result.score}</span>
                    <span>{result.providerName}</span>
                  </div>
                </div>
                {canWrite && (
                  <Button size="xs" onClick={() => download(result)}>
                    <DownloadIcon data-icon="inline-start" />
                    Download
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </Section>
      )}

      {outputs.length > 0 && (
        <Section title={`Awaiting review (${outputs.length})`}>
          <ul className="divide-y divide-border rounded-lg border border-border">
            {outputs.map((output) => (
              <li key={output.id} className="space-y-2 p-2.5">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <VariantBadges item={output} />
                    <Badge variant="outline">{output.format.toUpperCase()}</Badge>
                    <span className="text-xs text-muted-foreground">{output.detail}</span>
                  </div>
                  <div className="flex gap-1">
                    <Button size="xs" variant="ghost" onClick={() => setPreviewID(previewID === output.id ? '' : output.id)}>
                      {previewID === output.id ? 'Hide' : 'Preview'}
                    </Button>
                    {canWrite && (
                      <>
                        <Button size="xs" variant="secondary" onClick={() => applyOutput(output)}>
                          <SaveIcon data-icon="inline-start" />
                          Save
                        </Button>
                        <Button size="xs" variant="destructive" onClick={() => discardOutput(output)}>
                          <BanIcon data-icon="inline-start" />
                          Discard
                        </Button>
                      </>
                    )}
                  </div>
                </div>
                {previewID === output.id && (
                  <pre className="max-h-56 overflow-auto rounded-md border border-border bg-muted/40 p-2 text-xs whitespace-pre-wrap">
                    {summarise(output.payload ?? '')}
                  </pre>
                )}
              </li>
            ))}
          </ul>
        </Section>
      )}

      <Section title="Synchronize">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Subtitle file">
            <Select value={syncPath} onChange={(event) => setSyncPath(event.target.value)}>
              <option value="">Choose a sidecar…</option>
              {sidecars.map((sidecar) => (
                <option key={sidecar.path} value={sidecar.path}>
                  {sidecar.path.split('/').pop()}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Mode" hint="Offset and frame rate are instant; audio and reference use the bundled ffsubsync helper.">
            <Select value={syncMode} onChange={(event) => setSyncMode(event.target.value as typeof syncMode)}>
              <option value="offset">Adjust offset</option>
              <option value="fps">Convert frame rate</option>
              <option value="audio">Align to audio (VAD)</option>
              <option value="reference">Align to reference subtitle</option>
            </Select>
          </Field>
          {syncMode === 'offset' && (
            <Field label="Offset (seconds)">
              <Input value={offset} onChange={(event) => setOffset(event.target.value)} inputMode="decimal" />
            </Field>
          )}
          {syncMode === 'fps' && (
            <>
              <Field label="Source frame rate">
                <Input value={fpsFrom} onChange={(event) => setFpsFrom(event.target.value)} inputMode="decimal" />
              </Field>
              <Field label="Target frame rate">
                <Input value={fpsTo} onChange={(event) => setFpsTo(event.target.value)} inputMode="decimal" />
              </Field>
            </>
          )}
          {syncMode === 'reference' && (
            <Field label="Reference subtitle">
              <Select value={referencePath} onChange={(event) => setReferencePath(event.target.value)}>
                <option value="">Choose a reference…</option>
                {sidecars.map((sidecar) => (
                  <option key={sidecar.path} value={sidecar.path}>
                    {sidecar.path.split('/').pop()}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          {syncMode === 'audio' && (
            <Field label="Audio track" hint={streamsError || 'Default uses the first audio track.'}>
              <Select value={audioStream} onChange={(event) => setAudioStream(event.target.value)}>
                <option value="-1">Automatic</option>
                {audioStreams.map((stream) => (
                  <option key={stream.index} value={stream.index}>
                    {`#${stream.index} ${stream.language || 'und'} ${stream.codec}${stream.channels ? ` · ${stream.channels}ch` : ''}`}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          <Field label="Max offset (seconds)" hint="Leave blank to use the configured bound.">
            <Input value={maxOffset} onChange={(event) => setMaxOffset(event.target.value)} inputMode="decimal" />
          </Field>
          <Field label="Minimum alignment score" hint="0 rejects only anti-correlated alignments.">
            <Input value={minScore} onChange={(event) => setMinScore(event.target.value)} inputMode="decimal" />
          </Field>
        </div>
        {syncMode !== 'offset' && syncMode !== 'fps' && (
          <div className="grid gap-2 sm:grid-cols-2">
            <Toggle
              label="Golden-section frame rate search"
              hint="Try --gss for unusual frame rate ratios."
              checked={goldenSection}
              onChange={setGoldenSection}
            />
            <Toggle
              label="Do not guess frame rate"
              hint="Keep the subtitle frame rate fixed while finding the offset."
              checked={noFixFramerate}
              onChange={setNoFixFramerate}
            />
          </div>
        )}
        <Toggle
          label="Review before saving"
          hint="Stage the synchronized subtitle instead of replacing the current file."
          checked={syncPreview}
          onChange={setSyncPreview}
        />
        {canWrite && (
          <Button size="sm" onClick={runSync} disabled={!syncPath}>
            <PlayIcon data-icon="inline-start" />
            Run synchronization
          </Button>
        )}
      </Section>

      <Section title="Translate with AI">
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label="Source subtitle">
            <Select value={translatePath} onChange={(event) => setTranslatePath(event.target.value)}>
              <option value="">Choose a sidecar…</option>
              {sidecars.map((sidecar) => (
                <option key={sidecar.path} value={sidecar.path}>
                  {sidecar.path.split('/').pop()}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Target language" hint="ISO 639 code, for example de or pt-BR.">
            <Input value={translateLanguage} onChange={(event) => setTranslateLanguage(event.target.value)} />
          </Field>
          <div className="flex items-end">
            {canWrite && (
              <Button size="sm" onClick={runTranslate} disabled={!translatePath || !translateLanguage}>
                <LanguagesIcon data-icon="inline-start" />
                Translate
              </Button>
            )}
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          Translations are validated cue by cue and staged for review before a language sidecar is written.
        </p>
      </Section>

      <Section title="Embedded tracks">
        {streamsError && <p className="break-words text-xs text-amber-500">{streamsError}</p>}
        <div className="grid gap-3 sm:grid-cols-4">
          <Field label="Subtitle stream" hint="Forced and hearing-impaired flags come from the stream tags.">
            <Select value={extractStream} onChange={(event) => setExtractStream(event.target.value)}>
              <option value="-1">Choose a track…</option>
              {subtitleStreams.map((stream) => (
                <option key={stream.index} value={stream.index}>
                  {`#${stream.index} ${stream.language || 'und'} ${stream.codec}${stream.forced ? ' forced' : ''}${stream.hi ? ' SDH' : ''}`}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Language override" hint="Required when the track has no language tag.">
            <Input value={extractLanguage} onChange={(event) => setExtractLanguage(event.target.value)} placeholder="en" />
          </Field>
          <div className="flex items-end pb-2">
            <Toggle label="Review first" checked={extractPreview} onChange={setExtractPreview} />
          </div>
          <div className="flex items-end">
            {canWrite && (
              <Button size="sm" onClick={runExtract} disabled={Number(extractStream) < 0}>
                <DownloadIcon data-icon="inline-start" />
                Extract track
              </Button>
            )}
          </div>
        </div>
        {audioStreams.length > 0 && (
          <ul className="flex flex-wrap gap-2 text-xs text-muted-foreground">
            {audioStreams.map((stream) => (
              <li key={stream.index}>
                audio #{stream.index} {stream.language || 'und'} {stream.codec}
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section
        title="History"
        action={
          <Button size="xs" variant="ghost" onClick={() => reload().catch((cause) => setError(errorMessage(cause)))}>
            <RefreshCwIcon data-icon="inline-start" />
            Refresh
          </Button>
        }
      >
        {(detail?.history ?? []).length === 0 ? (
          <EmptyNote>No recorded subtitle activity for this video.</EmptyNote>
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
    </DialogShell>
  )
}
