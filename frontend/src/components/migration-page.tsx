import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { AlertTriangle, Check, ChevronLeft, ChevronRight, CircleAlert, LoaderCircle, Play, RotateCcw } from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage, request } from '@/lib/api'
import { useAuth } from '@/lib/auth-context'
import { moviesApi, type MovieProfile } from '@/lib/movies-api'
import {
  migrationApi,
  migrationAppCredentials,
  migrationAppLabels,
  migrationApps,
  type ConnectionTestResult,
  type ItemTotals,
  type MigrationApp,
  type MigrationApplyInput,
  type MigrationConnection,
  type MigrationPlan,
  type PlanItem,
} from '@/lib/migration-api'

type Draft = { enabled: boolean; url: string; apiKey: string; username: string; password: string }
type Options = Pick<
  MigrationApplyInput,
  'movies' | 'tv' | 'music' | 'files' | 'naming' | 'providers' | 'subtitles' | 'torrents' | 'torrentJobs' | 'createProfiles' | 'retryFailed'
>

const emptyDraft: Draft = { enabled: false, url: '', apiKey: '', username: '', password: '' }

function draftConnections(drafts: Record<MigrationApp, Draft>): MigrationConnection[] {
  return migrationApps
    .filter(app => drafts[app].enabled && drafts[app].url.trim() !== '')
    .map(app => ({
      app,
      url: drafts[app].url.trim(),
      ...(migrationAppCredentials[app].apiKey ? { apiKey: drafts[app].apiKey } : {}),
      ...(migrationAppCredentials[app].login
        ? { username: drafts[app].username, password: drafts[app].password }
        : {}),
    }))
}

// Source paths are mapping keys; the kind prefix only exists in local UI state.
function pathMapping(paths: Record<string, string>, kind: 'movies' | 'tv' | 'music' | 'torrent') {
  return Object.fromEntries(
    Object.entries(paths)
      .filter(([key]) => key.startsWith(`${kind}|`))
      .map(([key, value]) => [key.slice(kind.length + 1), value]),
  )
}

function mediaLabel(media: 'movies' | 'tv' | 'music') {
  if (media === 'tv') return 'TV'
  if (media === 'music') return 'Music'
  return 'Movies'
}

// Source paths and profile keys become element ids, so keep them to safe characters.
function elementID(prefix: string, value: string) {
  return `${prefix}-${value.replace(/[^a-zA-Z0-9_-]+/g, '-')}`
}

function Count({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border border-border px-3 py-2">
      <div className="text-lg font-semibold tabular-nums">{value}</div>
      <div className="text-xs text-muted-foreground">{label}</div>
    </div>
  )
}

function Notice({ tone, children }: { tone: 'ok' | 'error' | 'warn'; children: ReactNode }) {
  const styles = {
    ok: 'border-emerald-500/20 text-emerald-500',
    error: 'border-destructive/30 text-destructive',
    warn: 'border-amber-500/25 text-amber-500',
  }[tone]
  return (
    <p role={tone === 'error' ? 'alert' : 'status'} className={`rounded-lg border p-3 text-sm ${styles}`}>
      {children}
    </p>
  )
}

type MusicConfig = { qualityProfiles: { id: string; name: string; formats: string[] | null }[] }

export function MigrationPage() {
  const { can } = useAuth()
  const canReadSettings = can('settings.read')
  const [step, setStep] = useState<'connections' | 'review' | 'import'>('connections')
  const [drafts, setDrafts] = useState<Record<MigrationApp, Draft>>(
    () => Object.fromEntries(migrationApps.map(app => [app, { ...emptyDraft }])) as Record<MigrationApp, Draft>,
  )
  const [tests, setTests] = useState<Record<string, ConnectionTestResult>>({})
  const [plan, setPlan] = useState<MigrationPlan | null>(null)
  const [profiles, setProfiles] = useState<MovieProfile[]>([])
  const [musicProfiles, setMusicProfiles] = useState<MusicConfig['qualityProfiles']>([])
  const [rootPaths, setRootPaths] = useState<Record<string, string>>({})
  const [profileMap, setProfileMap] = useState<Record<string, string>>({})
  const [musicProfileMap, setMusicProfileMap] = useState<Record<string, string>>({})
  const [indexer, setIndexer] = useState('')
  const [usenet, setUsenet] = useState('')
  const [fallbacks, setFallbacks] = useState<string[]>([])
  const [torznab, setTorznab] = useState<string[]>([])
  const [redownload, setRedownload] = useState<string[]>([])
  const [options, setOptions] = useState<Options>({
    movies: true,
    tv: true,
    music: true,
    files: true,
    naming: true,
    providers: true,
    subtitles: true,
    torrents: true,
    torrentJobs: true,
    createProfiles: true,
    retryFailed: false,
  })
  const [totals, setTotals] = useState<ItemTotals | null>(null)
  const [applied, setApplied] = useState<PlanItem[]>([])
  const [failures, setFailures] = useState<PlanItem[]>([])
  const [busy, setBusy] = useState<'test' | 'preview' | 'apply' | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const connections = useMemo(() => draftConnections(drafts), [drafts])

  useEffect(() => {
    if (!canReadSettings) return
    const controller = new AbortController()
    moviesApi.profiles(controller.signal).then(setProfiles).catch(() => setProfiles([]))
    request<MusicConfig>('/music/config', { signal: controller.signal })
      .then(config => setMusicProfiles(config.qualityProfiles ?? []))
      .catch(() => setMusicProfiles([]))
    return () => controller.abort()
  }, [canReadSettings])

  function change(app: MigrationApp, patch: Partial<Draft>) {
    setDrafts(current => ({ ...current, [app]: { ...current[app], ...patch } }))
    setTests({})
    setNotice('')
  }

  async function testConnections() {
    setBusy('test')
    setError('')
    setTests({})
    try {
      const result = await migrationApi.testConnections(connections)
      setTests(Object.fromEntries(result.results.map(entry => [entry.app, entry])))
      const failed = result.results.filter(entry => !entry.ok)
      setNotice(failed.length === 0 ? 'All selected applications answered.' : `${failed.length} application(s) need attention.`)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function preview() {
    setBusy('preview')
    setError('')
    setNotice('')
    try {
      const created = await migrationApi.preview(connections)
      setPlan(created)
      const roots: Record<string, string> = {}
      for (const root of created.roots ?? []) roots[`${root.media}|${root.path}`] = root.suggestedLocalPath ?? ''
      for (const root of created.music?.roots ?? []) roots[`${root.media}|${root.path}`] = root.suggestedLocalPath ?? ''
      for (const transfer of created.torrents?.transfers ?? []) {
        if (transfer.downloadDir) roots[`torrent|${transfer.downloadDir}`] = roots[`torrent|${transfer.downloadDir}`] ?? ''
      }
      setRootPaths(roots)
      setProfileMap(Object.fromEntries((created.profiles ?? []).map(profile => [profile.key, profile.suggestedProfileId ?? ''])))
      setMusicProfileMap(Object.fromEntries((created.music?.profiles ?? []).map(profile => [profile.key, profile.suggestedProfileId ?? ''])))
      setIndexer((created.indexers ?? []).length === 1 ? created.indexers![0].key : '')
      setUsenet((created.usenetSources ?? []).length === 1 ? created.usenetSources![0].key : '')
      setFallbacks([])
      setTorznab((created.torznab ?? []).map(candidate => candidate.key))
      setRedownload([])
      setTotals(created.totals)
      setApplied([])
      setFailures(created.failures ?? [])
      setStep('review')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function apply() {
    if (!plan) return
    setBusy('apply')
    setError('')
    setNotice('')
    const collectedApplied: PlanItem[] = []
    const collectedFailures: PlanItem[] = []
    try {
      for (let round = 0; round < 40; round += 1) {
        const result = await migrationApi.apply(plan.id, {
          ...options,
          retryFailed: options.retryFailed && round === 0,
          moviesRoots: pathMapping(rootPaths, 'movies'),
          tvRoots: pathMapping(rootPaths, 'tv'),
          musicRoots: pathMapping(rootPaths, 'music'),
          torrentRoots: pathMapping(rootPaths, 'torrent'),
          profiles: profileMap,
          musicProfiles: musicProfileMap,
          indexer,
          usenet,
          usenetFallbacks: fallbacks,
          torznab,
          redownload,
          connections,
        })
        collectedApplied.push(...(result.applied ?? []))
        collectedFailures.push(...(result.failures ?? []))
        setTotals(result.totals)
        setApplied([...collectedApplied])
        setFailures([...collectedFailures])
        if (result.remaining === 0) {
          setNotice(result.message ?? '')
          break
        }
        setNotice(`Imported ${result.totals.done} of ${result.totals.total} items…`)
      }
      const refreshed = await migrationApi.getPlan(plan.id)
      setPlan(refreshed)
      setFailures(refreshed.failures ?? collectedFailures)
      setOptions(current => ({ ...current, retryFailed: false }))
    } catch (cause) {
      const message = errorMessage(cause)
      setError(message.includes('expired') ? `${message} Start a new preview to continue.` : message)
    } finally {
      setBusy(null)
    }
  }

  function startOver() {
    setPlan(null)
    setStep('connections')
    setTotals(null)
    setApplied([])
    setFailures([])
    setNotice('')
    setError('')
  }

  const stepIndex = step === 'connections' ? 0 : step === 'review' ? 1 : 2
  const started = totals !== null && totals.done + totals.skipped + totals.failed > 0
  const complete = totals !== null && totals.pending === 0 && plan?.status === 'applied'
  const usenetOptions = plan?.usenetSources ?? []
  const transfers = plan?.torrents?.transfers ?? []
  const incompleteWithProgress = transfers.filter(transfer => transfer.bytesDone > 0 && transfer.percentDone < 1)

  return (
    <div className="flex flex-col gap-6">
      <PageHeading
        title="Migration"
        description="Bring an existing Radarr, Sonarr, Lidarr, Prowlarr, Bazarr, SABnzbd, NZBGet, Transmission, or Jellyfin setup into Constellarr. Sources keep running unchanged."
      />

      <ol className="flex flex-wrap gap-2 text-sm" aria-label="Migration steps">
        {['Connections', 'Review and map', 'Import'].map((label, index) => (
          <li key={label} className="flex items-center gap-2">
            <Badge variant={index === stepIndex ? 'default' : 'outline'}>{index + 1}. {label}</Badge>
            {index < 2 && <ChevronRight className="size-3 text-muted-foreground" aria-hidden="true" />}
          </li>
        ))}
      </ol>

      {error && <Notice tone="error">{error}</Notice>}
      {!error && notice && <Notice tone="ok">{notice}</Notice>}

      {step === 'connections' && (
        <Card>
          <CardHeader>
            <CardTitle>Source applications</CardTitle>
            <CardDescription>
              Credentials are used for this migration only, kept on the server in memory, and never returned to the
              browser or written to the plan. Discovery is read-only.
            </CardDescription>
          </CardHeader>
          <CardContent className="gap-4">
            <fieldset className="flex flex-col gap-3">
              <legend className="sr-only">Choose the applications to migrate from</legend>
              {migrationApps.map(app => {
                const draft = drafts[app] ?? emptyDraft
                const credentials = migrationAppCredentials[app]
                const result = tests[app]
                return (
                  <div key={app} className="rounded-xl border border-border p-3">
                    <div className="flex flex-wrap items-center gap-3">
                      <input
                        id={`migration-${app}-enabled`}
                        type="checkbox"
                        className="size-4 accent-primary"
                        checked={draft.enabled}
                        onChange={event => change(app, { enabled: event.target.checked })}
                      />
                      <label htmlFor={`migration-${app}-enabled`} className="w-28 text-sm font-medium">
                        {migrationAppLabels[app]}
                      </label>
                      <div className="min-w-64 flex-1">
                        <label htmlFor={`migration-${app}-url`} className="sr-only">{migrationAppLabels[app]} URL</label>
                        <Input
                          id={`migration-${app}-url`}
                          value={draft.url}
                          inputMode="url"
                          placeholder="http://server:port"
                          disabled={!draft.enabled}
                          onChange={event => change(app, { url: event.target.value })}
                        />
                      </div>
                      {result && (
                        <span role={result.ok ? 'status' : 'alert'} className="text-xs">
                          {result.ok ? (
                            <span className="inline-flex items-center gap-1 text-emerald-500">
                              <Check className="size-3" aria-hidden="true" /> {result.version ? `v${result.version}` : 'reachable'}
                            </span>
                          ) : (
                            <span className="inline-flex items-center gap-1 text-destructive">
                              <CircleAlert className="size-3" aria-hidden="true" /> {result.error || 'not reachable'}
                            </span>
                          )}
                        </span>
                      )}
                    </div>
                    {draft.enabled && (
                      <div className="mt-3 grid gap-3 pl-8 sm:grid-cols-3">
                        {credentials.apiKey && (
                          <div className="space-y-1">
                            <label htmlFor={`migration-${app}-key`} className="text-xs font-medium">API key</label>
                            <Input
                              id={`migration-${app}-key`}
                              type="password"
                              autoComplete="off"
                              value={draft.apiKey}
                              onChange={event => change(app, { apiKey: event.target.value })}
                            />
                          </div>
                        )}
                        {credentials.login && (
                          <>
                            <div className="space-y-1">
                              <label htmlFor={`migration-${app}-user`} className="text-xs font-medium">Username</label>
                              <Input
                                id={`migration-${app}-user`}
                                autoComplete="off"
                                value={draft.username}
                                onChange={event => change(app, { username: event.target.value })}
                              />
                            </div>
                            <div className="space-y-1">
                              <label htmlFor={`migration-${app}-password`} className="text-xs font-medium">Password</label>
                              <Input
                                id={`migration-${app}-password`}
                                type="password"
                                autoComplete="off"
                                value={draft.password}
                                onChange={event => change(app, { password: event.target.value })}
                              />
                            </div>
                          </>
                        )}
                      </div>
                    )}
                  </div>
                )
              })}
            </fieldset>
          </CardContent>
          <CardContent>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={testConnections} disabled={busy !== null || connections.length === 0}>
                {busy === 'test' ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
                Test connections
              </Button>
              <Button onClick={preview} disabled={busy !== null || connections.length === 0}>
                {busy === 'preview' ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
                Preview migration
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {step === 'review' && plan && (
        <div className="flex flex-col gap-6">
          <Card>
            <CardHeader>
              <CardTitle>What will be imported</CardTitle>
              <CardDescription>
                Counted from the connected applications when the preview ran. The plan is pinned to that data and expires{' '}
                {new Date(plan.expiresAt).toLocaleString()}.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                <Count label="Movies" value={plan.counts.movies} />
                <Count label="Series" value={plan.counts.series} />
                <Count label="Artists" value={plan.counts.artists} />
                <Count label="Albums" value={plan.counts.albums} />
                <Count label="Existing files" value={plan.counts.files} />
                <Count label="Profiles" value={plan.counts.profiles} />
                <Count label="Source roots" value={plan.counts.roots} />
                <Count label="Indexers" value={plan.counts.indexers} />
                <Count label="News servers" value={plan.counts.newsServers} />
                <Count label="Torrents" value={plan.counts.torrents} />
                <Count label="Subtitle languages" value={plan.counts.languages} />
                <Count label="Work items" value={plan.totals.total} />
              </div>
            </CardContent>
          </Card>

          {(plan.roots ?? []).length + (plan.music?.roots ?? []).length + (transfers.length > 0 ? 1 : 0) > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>Path mapping</CardTitle>
                <CardDescription>
                  Map each source folder to where the same files live on this server. Files that are not present locally
                  are never reported as available, and torrent data is only reused after it verifies.
                </CardDescription>
              </CardHeader>
              <CardContent className="gap-3">
                {[...(plan.roots ?? []), ...(plan.music?.roots ?? [])].map(root => {
                  const key = `${root.media}|${root.path}`
                  return (
                    <div key={key} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] sm:items-center">
                      <div className="min-w-0">
                        <div className="truncate text-sm font-medium">{mediaLabel(root.media)} · {root.path}</div>
                        <div className="text-xs text-muted-foreground">
                          {root.source} {root.accessible ? '' : '· reported inaccessible by the source'}
                        </div>
                      </div>
                      <div>
                        <label htmlFor={elementID('root', key)} className="sr-only">Local folder for {root.path}</label>
                        <Input
                          id={elementID('root', key)}
                          value={rootPaths[key] ?? ''}
                          placeholder="/data/media"
                          onChange={event => setRootPaths(current => ({ ...current, [key]: event.target.value }))}
                        />
                      </div>
                    </div>
                  )
                })}
                {transfers.length > 0 && (
                  <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] sm:items-center">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">Transmission · {plan.torrents?.downloadDir}</div>
                      <div className="text-xs text-muted-foreground">Where the downloaded torrent data lives on this server</div>
                    </div>
                    <div>
                      <label htmlFor={elementID('root', `torrent|${plan.torrents?.downloadDir ?? ''}`)} className="sr-only">
                        Local folder for Transmission downloads
                      </label>
                      <Input
                        id={elementID('root', `torrent|${plan.torrents?.downloadDir ?? ''}`)}
                        value={rootPaths[`torrent|${plan.torrents?.downloadDir ?? ''}`] ?? ''}
                        placeholder="/data/downloads"
                        onChange={event =>
                          setRootPaths(current => ({ ...current, [`torrent|${plan.torrents?.downloadDir ?? ''}`]: event.target.value }))
                        }
                      />
                    </div>
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {(plan.profiles ?? []).length + (plan.music?.profiles ?? []).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>Quality profiles</CardTitle>
                <CardDescription>
                  Map each source profile, or let Constellarr create one from the source qualities. Existing profiles are
                  never overwritten.
                </CardDescription>
              </CardHeader>
              <CardContent className="gap-3">
                {(plan.profiles ?? []).map(profile => (
                  <div key={profile.key} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,14rem)] sm:items-center">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">{profile.source} · {profile.name}</div>
                      <div className="text-xs text-muted-foreground">
                        {(profile.qualities ?? []).join(', ') || 'no supported qualities'}
                        {profile.unsupported?.length ? ` · unsupported: ${profile.unsupported.join(', ')}` : ''}
                      </div>
                    </div>
                    <div>
                      <label htmlFor={elementID('profile', profile.key)} className="sr-only">Constellarr profile for {profile.name}</label>
                      <select
                        id={elementID('profile', profile.key)}
                        className="h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm dark:bg-input/30"
                        value={profileMap[profile.key] ?? ''}
                        onChange={event => setProfileMap(current => ({ ...current, [profile.key]: event.target.value }))}
                      >
                        <option value="">Create from source settings</option>
                        {profiles.map(option => (
                          <option key={option.id} value={option.id}>{option.name}</option>
                        ))}
                      </select>
                      {!profile.suggestedProfileId && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          No exact equivalent was found, so the source qualities are kept as they are.
                        </p>
                      )}
                    </div>
                  </div>
                ))}
                {(plan.music?.profiles ?? []).map(profile => (
                  <div key={profile.key} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,14rem)] sm:items-center">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">Music · {profile.name}</div>
                      <div className="text-xs text-muted-foreground">
                        {(profile.formats ?? []).join(', ') || 'no supported formats'}
                        {profile.minBitrateKbps > 0 ? ` · min ${profile.minBitrateKbps} kbps` : ''}
                        {profile.unsupported?.length ? ` · unsupported: ${profile.unsupported.join(', ')}` : ''}
                      </div>
                    </div>
                    <div>
                      <label htmlFor={elementID('music-profile', profile.key)} className="sr-only">Music profile for {profile.name}</label>
                      <select
                        id={elementID('music-profile', profile.key)}
                        className="h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm dark:bg-input/30"
                        value={musicProfileMap[profile.key] ?? ''}
                        onChange={event => setMusicProfileMap(current => ({ ...current, [profile.key]: event.target.value }))}
                      >
                        <option value="">Create or reuse by name</option>
                        {musicProfiles.map(option => (
                          <option key={option.id} value={option.id}>{option.name}</option>
                        ))}
                      </select>
                      {!profile.suggestedProfileId && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          No exact equivalent was found, so the source formats are kept as they are.
                        </p>
                      )}
                    </div>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}

          {(plan.indexers ?? []).length + usenetOptions.length + (plan.torznab ?? []).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>Indexers and news servers</CardTitle>
                <CardDescription>
                  Constellarr serves one NZB indexer and one news account today; torrent indexers are added to the built-in
                  torrent module and can be selected freely.
                </CardDescription>
              </CardHeader>
              <CardContent className="gap-4">
                {(plan.indexers ?? []).length > 0 && (
                  <div className="space-y-2">
                    <div className="text-sm font-medium">NZB indexer</div>
                    {(plan.indexers ?? []).map(candidate => (
                      <label key={candidate.key} htmlFor={elementID('indexer', candidate.key)} className="flex items-center gap-2 text-sm">
                        <input
                          id={elementID('indexer', candidate.key)}
                          type="radio"
                          name="migration-indexer"
                          className="size-4 accent-primary"
                          checked={indexer === candidate.key}
                          onChange={() => setIndexer(candidate.key)}
                        />
                        {candidate.name} · {candidate.url}
                      </label>
                    ))}
                  </div>
                )}
                {usenetOptions.length > 0 && (
                  <div className="space-y-2">
                    <div className="text-sm font-medium">Primary news server</div>
                    {usenetOptions.map(candidate => (
                      <label key={candidate.key} htmlFor={elementID('usenet', candidate.key)} className="flex items-center gap-2 text-sm">
                        <input
                          id={elementID('usenet', candidate.key)}
                          type="radio"
                          name="migration-usenet"
                          className="size-4 accent-primary"
                          checked={usenet === candidate.key}
                          onChange={() => {
                            setUsenet(candidate.key)
                            setFallbacks(current => current.filter(key => key !== candidate.key))
                          }}
                        />
                        {candidate.source} · {candidate.host}:{candidate.port} · {candidate.connections} connections
                        {candidate.notes?.length ? <span className="text-muted-foreground">({candidate.notes.join('; ')})</span> : null}
                      </label>
                    ))}
                    {usenet && usenetOptions.length > 1 && (
                      <fieldset className="space-y-2 pl-6">
                        <legend className="text-xs font-medium text-muted-foreground">
                          Fallback hosts (only sources sharing the primary account credentials)
                        </legend>
                        {usenetOptions
                          .filter(candidate => candidate.key !== usenet)
                          .map(candidate => (
                            <label key={candidate.key} htmlFor={elementID('fallback', candidate.key)} className="flex items-center gap-2 text-sm">
                              <input
                                id={elementID('fallback', candidate.key)}
                                type="checkbox"
                                className="size-4 accent-primary"
                                checked={fallbacks.includes(candidate.key)}
                                onChange={event =>
                                  setFallbacks(current =>
                                    event.target.checked ? [...current, candidate.key] : current.filter(key => key !== candidate.key),
                                  )
                                }
                              />
                              {candidate.host}
                            </label>
                          ))}
                      </fieldset>
                    )}
                  </div>
                )}
                {(plan.torznab ?? []).length > 0 && (
                  <fieldset className="space-y-2">
                    <legend className="text-sm font-medium">Torrent indexers for the torrent module</legend>
                    {(plan.torznab ?? []).map(candidate => (
                      <label key={candidate.key} htmlFor={elementID('torznab', candidate.key)} className="flex items-center gap-2 text-sm">
                        <input
                          id={elementID('torznab', candidate.key)}
                          type="checkbox"
                          className="size-4 accent-primary"
                          checked={torznab.includes(candidate.key)}
                          onChange={event =>
                            setTorznab(current =>
                              event.target.checked ? [...current, candidate.key] : current.filter(key => key !== candidate.key),
                            )
                          }
                        />
                        {candidate.name}
                      </label>
                    ))}
                  </fieldset>
                )}
              </CardContent>
            </Card>
          )}

          {plan.subtitles && (
            <Card>
              <CardHeader>
                <CardTitle>Subtitles</CardTitle>
                <CardDescription>Bazarr languages, providers, scoring, and alignment limits are applied to the subtitle service.</CardDescription>
              </CardHeader>
              <CardContent className="text-sm text-muted-foreground">
                <ul className="space-y-1">
                  <li>Languages: {(plan.subtitles.languages ?? []).join(', ') || 'none enabled in Bazarr'}</li>
                  {(plan.subtitles.profiles ?? []).map(profile => (
                    <li key={profile.key}>
                      Profile {profile.name}: {(profile.languages ?? []).map(language => language.code).join(', ') || 'no supported languages'}
                    </li>
                  ))}
                  {(plan.subtitles.providerPlans ?? []).map(provider => (
                    <li key={provider.key}>
                      {provider.name}
                      {provider.type ? ` → ${provider.type}` : ' (no Constellarr adapter, stays in Bazarr)'}
                      {provider.usernameSet ? ' · account imported' : ''}
                    </li>
                  ))}
                  <li>
                    Score cutoff {plan.subtitles.minimumScore} · sync offset limit {plan.subtitles.sync.maxOffsetSeconds}s
                  </li>
                </ul>
              </CardContent>
            </Card>
          )}

          {plan.torrents && (
            <Card>
              <CardHeader>
                <CardTitle>Torrents</CardTitle>
                <CardDescription>
                  Speed and seeding settings are copied. Torrents are added paused; existing data is reused only after it
                  verifies locally.
                </CardDescription>
              </CardHeader>
              <CardContent className="gap-3 text-sm">
                <p className="text-muted-foreground">
                  {plan.torrents.speedDownEnabled ? `Download limit ${plan.torrents.speedLimitDown} KB/s` : 'No download limit'}
                  {' · '}
                  {plan.torrents.speedUpEnabled ? `Upload limit ${plan.torrents.speedLimitUp} KB/s` : 'No upload limit'}
                  {' · '}
                  {plan.torrents.seedRatioLimited ? `Seed ratio ${plan.torrents.seedRatioLimit}` : 'Seed ratio kept'}
                </p>
                {transfers.map(transfer => (
                  <label key={transfer.hash} htmlFor={elementID('redownload', transfer.hash)} className="flex items-center gap-2">
                    <input
                      id={elementID('redownload', transfer.hash)}
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={redownload.includes(transfer.hash)}
                      onChange={event =>
                        setRedownload(current =>
                          event.target.checked ? [...current, transfer.hash] : current.filter(hash => hash !== transfer.hash),
                        )
                      }
                    />
                    <span className="min-w-0 flex-1 truncate">{transfer.name}</span>
                    <span className="text-xs text-muted-foreground">
                      {Math.round(transfer.percentDone * 100)}% · {redownload.includes(transfer.hash) ? 'download again' : 'reuse local data when it verifies'}
                    </span>
                  </label>
                ))}
                {incompleteWithProgress.length > 0 && (
                  <p className="text-xs text-amber-500">
                    {incompleteWithProgress.length} torrent(s) have partial progress. Without a verified local mapping they are left
                    out unless you tick download again.
                  </p>
                )}
              </CardContent>
            </Card>
          )}

          {(plan.warnings ?? []).length > 0 && (
            <Notice tone="warn">
              <span className="flex items-start gap-2">
                <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                <span>{(plan.warnings ?? []).join(' ')}</span>
              </span>
            </Notice>
          )}

          {(plan.unsupported ?? []).length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>Reported as unsupported</CardTitle>
                <CardDescription>These settings are not silently discarded; they stay in the source application and are listed here.</CardDescription>
              </CardHeader>
              <CardContent>
                <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
                  {(plan.unsupported ?? []).map((entry, index) => (
                    <li key={`${entry.area}-${index}`}><span className="text-foreground">{entry.area}:</span> {entry.detail}</li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          )}

          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => setStep('connections')} disabled={busy !== null}>
              <ChevronLeft aria-hidden="true" /> Back
            </Button>
            <Button onClick={() => setStep('import')} disabled={busy !== null}>
              Continue to import <ChevronRight aria-hidden="true" />
            </Button>
          </div>
        </div>
      )}

      {step === 'import' && plan && (
        <div className="flex flex-col gap-6">
          <Card>
            <CardHeader>
              <CardTitle>Import</CardTitle>
              <CardDescription>
                Catalog identifiers, monitoring, tags, profiles, root paths, naming, providers, subtitles, and locally
                available files are imported. Sources stay unchanged; nothing is deleted.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <fieldset className="grid gap-2 sm:grid-cols-2">
                <legend className="sr-only">Import options</legend>
                {([
                  ['movies', 'Movie catalog'],
                  ['tv', 'TV catalog'],
                  ['music', 'Music catalog'],
                  ['files', 'Existing local files'],
                  ['naming', 'Naming settings'],
                  ['providers', 'Indexer and news server settings'],
                  ['subtitles', 'Subtitle settings'],
                  ['torrents', 'Torrent settings and indexers'],
                  ['torrentJobs', 'Transmission torrents'],
                  ['createProfiles', 'Create quality profiles from sources'],
                  ['retryFailed', 'Retry previously failed items'],
                ] as const).map(([key, label]) => (
                  <label key={key} htmlFor={`migration-option-${key}`} className="flex items-center gap-2 text-sm">
                    <input
                      id={`migration-option-${key}`}
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={options[key]}
                      disabled={busy === 'apply'}
                      onChange={event => setOptions(current => ({ ...current, [key]: event.target.checked }))}
                    />
                    {label}
                  </label>
                ))}
              </fieldset>
            </CardContent>
            <CardContent>
              <div className="flex flex-wrap items-center gap-2">
                <Button onClick={apply} disabled={busy !== null || complete}>
                  {busy === 'apply' ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : complete ? <Check aria-hidden="true" /> : <Play aria-hidden="true" />}
                  {complete ? 'Import complete' : started ? 'Continue import' : 'Start import'}
                </Button>
                <Button variant="outline" onClick={startOver} disabled={busy !== null}>
                  <RotateCcw aria-hidden="true" /> Start a new migration
                </Button>
                <Button variant="ghost" onClick={() => setStep('review')} disabled={busy !== null}>
                  <ChevronLeft aria-hidden="true" /> Back to review
                </Button>
              </div>
              {totals && (
                <div className="mt-4 space-y-2" aria-live="polite">
                  <p className="text-sm">
                    {totals.done} imported · {totals.failed} failed · {totals.skipped} skipped · {totals.pending} remaining
                    {busy === 'apply' ? ' — working…' : ''}
                  </p>
                  <div className="h-2 w-full overflow-hidden rounded-full bg-muted" role="presentation">
                    <div
                      className="h-full bg-primary transition-[width]"
                      style={{ width: `${totals.total === 0 ? 0 : Math.round(((totals.done + totals.skipped + totals.failed) / totals.total) * 100)}%` }}
                    />
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Plan status: {plan.status}{plan.status === 'partial' ? ' — fix the listed items, then retry failed items.' : ''}
                  </p>
                </div>
              )}
            </CardContent>
          </Card>

          {failures.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>Items that need attention</CardTitle>
                <CardDescription>Nothing was deleted; fix the cause and retry these items.</CardDescription>
              </CardHeader>
              <CardContent>
                <ul className="space-y-2 text-sm">
                  {failures.slice(0, 200).map(item => (
                    <li key={`${item.position}-${item.target}`} className="rounded-lg border border-destructive/30 p-3">
                      <div className="font-medium">{item.label}</div>
                      <div className="text-destructive">{item.message}</div>
                    </li>
                  ))}
                </ul>
                {failures.length > 200 && <p className="text-xs text-muted-foreground">Showing the first 200 of {failures.length} items.</p>}
              </CardContent>
            </Card>
          )}

          {applied.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>Imported items</CardTitle>
                <CardDescription>{applied.length} items were applied in this run.</CardDescription>
              </CardHeader>
              <CardContent>
                <ul className="space-y-1 text-sm text-muted-foreground">
                  {applied.slice(0, 100).map(item => (
                    <li key={`${item.position}-${item.target}`}>
                      <span className="text-foreground">{item.label}</span> — {item.message}
                    </li>
                  ))}
                </ul>
                {applied.length > 100 && <p className="text-xs text-muted-foreground">Showing the first 100 of {applied.length} applied items.</p>}
              </CardContent>
            </Card>
          )}

          {busy === null && totals && totals.pending === 0 && (
            <Notice tone={totals.failed > 0 ? 'warn' : 'ok'}>
              {totals.failed > 0
                ? `Migration finished with ${totals.failed} items needing attention.`
                : 'Migration finished. Your source applications are unchanged and can keep running.'}
            </Notice>
          )}
        </div>
      )}
    </div>
  )
}
