import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Dialog } from 'radix-ui'
import {
  CalendarIcon,
  CheckIcon,
  CircleAlertIcon,
  Disc3Icon,
  DownloadIcon,
  FolderSearchIcon,
  HistoryIcon,
  ListMusicIcon,
  LoaderCircleIcon,
  MusicIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SaveIcon,
  SearchIcon,
  SettingsIcon,
  Trash2Icon,
  XIcon,
} from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { formatAge, formatBytes } from '@/lib/format'
import {
  musicApi,
  type AddArtistInput,
  type Album,
  type AlbumResult,
  type Artist,
  type ArtistResult,
  type Candidate,
  type HistoryEntry,
  type MusicConfig,
  type MusicConnectionTests,
  type MusicRelease,
  type QualityProfile,
} from '@/lib/music-api'

const tabs = ['library', 'wanted', 'calendar', 'history', 'settings'] as const
type Tab = (typeof tabs)[number]

const tabLabels: Record<Tab, string> = {
  library: 'Library',
  wanted: 'Wanted',
  calendar: 'Calendar',
  history: 'History',
  settings: 'Settings',
}

const statusLabels: Record<string, string> = {
  available: 'Available',
  downloading: 'Downloading',
  paused: 'Paused',
  cancelled: 'Cancelled',
  wanted: 'Wanted',
  'cutoff-unmet': 'Below cutoff',
  'import-failed': 'Import failed',
  failed: 'Failed',
  unmonitored: 'Unmonitored',
  imported: 'Imported',
  completed: 'Completed',
}

const statusTones: Record<string, string> = {
  available: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300',
  completed: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300',
  downloading: 'border-primary/30 bg-primary/10 text-primary',
  wanted: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  paused: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  'cutoff-unmet': 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  'import-failed': 'border-destructive/30 bg-destructive/10 text-destructive',
  failed: 'border-destructive/30 bg-destructive/10 text-destructive',
  cancelled: 'border-border bg-muted/60 text-muted-foreground',
  unmonitored: 'border-border bg-muted/60 text-muted-foreground',
}

const monitorOptions = [
  { value: 'all', label: 'All releases' },
  { value: 'future', label: 'Future releases' },
  { value: 'missing', label: 'Missing releases' },
  { value: 'none', label: 'None' },
]

const monitorOptionLabels: Record<string, string> = {
  all: 'all releases',
  future: 'future releases',
  missing: 'missing releases',
  none: 'no releases',
}

const importModes = ['copy', 'hardlink', 'move']
const knownFormats = ['flac', 'alac', 'wav', 'aiff', 'ape', 'dsd', 'wavpack', 'opus', 'vorbis', 'aac', 'mp3', 'wma']

function notice(cause: unknown) {
  return errorMessage(cause)
}

function queueTarget(protocol: string | undefined) {
  return protocol === 'torrent' ? { queue: 'Torrents', href: '#torrents' } : { queue: 'Usenet', href: '#usenet' }
}

function fileName(path: string) {
  return path.split('/').filter(Boolean).pop() ?? path
}

function useMusicPoll(load: (signal: AbortSignal) => Promise<unknown>, active = true) {
  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    let timer: number | undefined
    const tick = async () => {
      if (document.visibilityState === 'visible') await load(controller.signal)
      if (!controller.signal.aborted) timer = window.setTimeout(tick, 5000)
    }
    void tick()
    return () => {
      controller.abort()
      window.clearTimeout(timer)
    }
  }, [active, load])
}

function StatusBadge({ status }: { status: string }) {
  return (
    <Badge variant="outline" className={statusTones[status] ?? statusTones.unmonitored}>
      {statusLabels[status] ?? status.replace(/[-_]/g, ' ')}
    </Badge>
  )
}

function useMusicRoute() {
  const [routeActive, setRouteActive] = useState(true)
  useEffect(() => {
    const update = () => {
      const hash = window.location.hash.replace(/^#\/?/, '')
      setRouteActive(!hash || hash === 'music')
    }
    update()
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])
  return routeActive
}

function Modal({
  title,
  description,
  onClose,
  wide = false,
  children,
}: {
  title: string
  description: string
  onClose: () => void
  wide?: boolean
  children: React.ReactNode
}) {
  return (
    <Dialog.Root open onOpenChange={(next) => !next && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60" />
        <Dialog.Content
          className={`fixed top-1/2 left-1/2 z-50 flex max-h-[90vh] w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-border bg-card shadow-xl ${
            wide ? 'max-w-4xl' : 'max-w-2xl'
          }`}
        >
          <header className="flex items-start justify-between gap-4 border-b border-border p-4">
            <div className="min-w-0">
              <Dialog.Title className="font-heading text-base font-semibold">{title}</Dialog.Title>
              <Dialog.Description className="mt-0.5 text-xs text-muted-foreground">{description}</Dialog.Description>
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

function ErrorNotice({ message }: { message: string }) {
  if (!message) return null
  return (
    <p className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 p-2 text-xs text-destructive">
      <CircleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
      {message}
    </p>
  )
}

function SuccessNotice({ message, link }: { message: string; link?: { href: string; queue: string } }) {
  if (!message) return null
  return (
    <p
      role="status"
      className="flex items-start gap-2 rounded-md border border-emerald-500/20 bg-emerald-500/5 p-2 text-xs text-emerald-400"
    >
      <CheckIcon className="mt-0.5 size-3.5 shrink-0" />
      <span>
        {message}
        {link ? (
          <>
            {' '}
            <a
              href={link.href}
              className="underline underline-offset-4"
              aria-label={`View the queued download in ${link.queue}`}
            >
              View download
            </a>
            .
          </>
        ) : null}
      </span>
    </p>
  )
}

function AlbumDialog({
  albumId,
  canWrite,
  canReadSettings,
  onClose,
  onChanged,
}: {
  albumId: string
  canWrite: boolean
  canReadSettings: boolean
  onClose: () => void
  onChanged: () => void
}) {
  const [album, setAlbum] = useState<Album | null>(null)
  const [config, setConfig] = useState<MusicConfig | null>(null)
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [releases, setReleases] = useState<MusicRelease[] | null>(null)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [queued, setQueued] = useState<{ queue: string; href: string } | null>(null)
  const [busy, setBusy] = useState('')
  const [confirmRelease, setConfirmRelease] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [recycleFiles, setRecycleFiles] = useState(false)
  const { can } = useAuth()
  // The queue pages need downloads.read; without it the link would only redirect to the overview.
  const canReadQueue = can(accessPermissions.downloadsRead)

  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        // Configuration needs settings.read; the album itself only needs library.read.
        const [detail, events, cfg] = await Promise.all([
          musicApi.album(albumId, signal),
          musicApi.albumHistory(albumId, signal),
          canReadSettings ? musicApi.config(signal) : Promise.resolve(null),
        ])
        if (signal?.aborted) return
        setAlbum(detail)
        setHistory(events)
        if (cfg) setConfig(cfg)
      } catch (cause) {
        if (signal?.aborted) return
        setError(notice(cause))
      }
    },
    [albumId, canReadSettings],
  )

  useMusicPoll(load)

  const act = async (name: string, action: () => Promise<unknown>, message = '') => {
    setBusy(name)
    setError('')
    setSuccess('')
    setQueued(null)
    try {
      await action()
      if (message) setSuccess(message)
      await load()
      onChanged()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const downloadRelease = async (release: MusicRelease) => {
    setBusy('download')
    setError('')
    setSuccess('')
    setQueued(null)
    try {
      await musicApi.grab(albumId, release.id, !release.decision.allowed)
      setConfirmRelease('')
      setSuccess(`Download queued: ${release.title}`)
      setQueued(queueTarget(release.protocol))
      await load()
      onChanged()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const renameFiles = async () => {
    setBusy('rename')
    setError('')
    setSuccess('')
    setQueued(null)
    try {
      const result = await musicApi.rename(albumId, false)
      if (result.files.length === 0) setError('Nothing to rename with the current templates.')
      else setSuccess(`Renamed ${result.files.length} file${result.files.length === 1 ? '' : 's'}.`)
      await load()
      onChanged()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const deleteAlbum = async () => {
    setBusy('delete')
    setError('')
    setSuccess('')
    setQueued(null)
    try {
      await musicApi.removeAlbum(albumId, recycleFiles)
      onChanged()
      onClose()
    } catch (cause) {
      setError(notice(cause))
      setBusy('')
    }
  }

  if (!album) {
    return (
      <Modal title="Album" description="Loading album details" onClose={onClose}>
        <p className="text-sm text-muted-foreground">Loading…</p>
      </Modal>
    )
  }
  const files = album.files ?? []
  const tracks = album.tracks ?? []
  const profile = config?.qualityProfiles.find((item) => item.id === album.profileId)
  const downloading = album.status === 'downloading' || album.status === 'importing'

  return (
    <Modal title={album.title} description={`${album.artistName || 'Unknown artist'}${album.year ? ` · ${album.year}` : ''}`} onClose={onClose} wide>
      <div className="flex flex-col gap-4">
        <ErrorNotice message={error} />
        <SuccessNotice message={success} link={canReadQueue ? queued ?? undefined : undefined} />
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge status={album.status} />
          <Badge variant="outline">{album.type || 'album'}</Badge>
          {album.format ? <Badge variant="outline">{album.format.toUpperCase()}</Badge> : null}
          {album.size > 0 ? <Badge variant="outline">{formatBytes(album.size)}</Badge> : null}
          {album.musicBrainzId ? <Badge variant="outline">MusicBrainz match</Badge> : null}
          <Badge variant="outline">{album.monitored ? 'Automatic downloads on' : 'Automatic downloads off'}</Badge>
        </div>
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={busy !== ''}
              onClick={() =>
                void act('search', async () => {
                  const found = canWrite ? await musicApi.searchAlbum(album.id) : await musicApi.releaseSearch('', album.id)
                  setReleases(found)
                })
              }
            >
              {busy === 'search' ? <LoaderCircleIcon className="animate-spin" /> : <SearchIcon />}
              Search releases
            </Button>
            <Button
              size="sm"
              variant="outline"
              aria-pressed={album.monitored}
              disabled={busy !== '' || !canWrite}
              title={
                album.monitored
                  ? 'Turn automatic downloads off for this album.'
                  : 'Search indexers and download releases for this album automatically.'
              }
              onClick={() =>
                void act(
                  'monitor',
                  () => musicApi.monitorAlbum(album.id, !album.monitored, album.profileId),
                  album.monitored ? 'Automatic downloads turned off.' : 'Automatic downloads turned on.',
                )
              }
            >
              <DownloadIcon /> Download automatically
            </Button>
            <Button size="sm" variant="outline" disabled={busy !== '' || !canWrite} onClick={() => void act('refresh', () => musicApi.refreshAlbum(album.id), 'Metadata refreshed.')}>
              <RefreshCwIcon /> Refresh metadata
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={busy !== '' || !canWrite || files.length === 0}
              title={files.length === 0 ? 'No imported files to rename yet.' : undefined}
              aria-label={`Rename ${album.title} files on disk`}
              onClick={() => void renameFiles()}
            >
              {busy === 'rename' ? <LoaderCircleIcon className="animate-spin" /> : <PencilIcon />} Rename files
            </Button>
            {confirmDelete ? (
              <span className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-1.5">
                <span className="flex flex-col gap-1 text-xs text-muted-foreground">
                  <span className="text-sm">Remove this album from the catalog?</span>
                  <span>Imported files stay on disk unless you recycle them.</span>
                  <label className="flex items-center gap-2">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={recycleFiles}
                      onChange={(event) => setRecycleFiles(event.target.checked)}
                    />
                    Move album files to .recycle
                  </label>
                </span>
                <Button size="sm" variant="outline" onClick={() => setConfirmDelete(false)}>
                  Cancel
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={busy !== '' || !canWrite}
                  aria-label={`Remove ${album.title} from the catalog`}
                  onClick={() => void deleteAlbum()}
                >
                  {busy === 'delete' ? <LoaderCircleIcon className="animate-spin" /> : <Trash2Icon />}
                  Remove
                </Button>
              </span>
            ) : (
              <Button
                size="sm"
                variant="outline"
                className="text-destructive"
                disabled={busy !== '' || !canWrite}
                aria-expanded={confirmDelete}
                onClick={() => setConfirmDelete(true)}
              >
                <Trash2Icon /> Remove from catalog
              </Button>
            )}
          </div>
          <p className="text-[11px] text-muted-foreground" role="status">
            {album.monitored
              ? 'Searches indexers and downloads releases for this album without asking again.'
              : 'Nothing is searched or downloaded automatically. Choose a release below or turn this on.'}
          </p>
        </div>
        {config && config.qualityProfiles.length > 0 ? (
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            Quality profile
            <select
              className="h-8 rounded-md border border-input bg-background px-2 text-xs"
              value={album.profileId}
              disabled={!canWrite}
              onChange={(event) => void act('profile', () => musicApi.monitorAlbum(album.id, album.monitored, event.target.value), 'Quality profile updated.')}
            >
              {config.qualityProfiles.map((item) => (
                <option key={item.id} value={item.id}>{item.name}</option>
              ))}
            </select>
            {profile ? <span>{profile.losslessOnly ? 'lossless only' : profile.formats.join(' → ') || 'any format'}</span> : null}
          </label>
        ) : null}
        {!canWrite ? (
          <p className="text-[11px] text-muted-foreground">Your account can browse this library but not change it.</p>
        ) : null}
        {album.error ? <p className="text-xs text-destructive">{album.error}</p> : null}

        {releases ? (
          <section className="flex flex-col gap-2">
            <h3 className="text-sm font-medium">Releases</h3>
            <p className="text-[11px] text-muted-foreground">
              Sizes and profile decisions are shown for each candidate. Nothing downloads until you choose a release.
            </p>
            {releases.length === 0 ? <p className="text-xs text-muted-foreground">No releases found.</p> : null}
            <ul className="flex flex-col gap-1">
              {releases.map((release) => (
                <li key={release.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-2">
                  <div className="min-w-0">
                    <p className="truncate text-xs font-medium" title={release.title}>{release.title}</p>
                    <p className="text-[11px] text-muted-foreground">
                      {release.decision.format?.toUpperCase() || 'unknown'}
                      {release.decision.bitrateKbps ? ` ${release.decision.bitrateKbps} kbps` : ''}
                      {release.size ? ` · ${formatBytes(release.size)}` : ''} · {formatAge(release.published)} · score {release.decision.score}
                      {release.protocol === 'torrent' ? ` · ${release.source || 'torrent'} · ${release.seeders ?? 0} seeders` : ''}
                    </p>
                    {release.decision.reasons?.length ? (
                      <p className="text-[11px] text-amber-300">{release.decision.reasons.join('; ')}</p>
                    ) : null}
                    {confirmRelease === release.id ? (
                      <p className="mt-1 flex items-center gap-2 text-[11px] text-amber-300">
                        <CircleAlertIcon className="size-3.5 shrink-0" />
                        This release is below the profile requirements. Download it anyway?
                        <Button
                          size="xs"
                          variant="outline"
                          aria-label={`Confirm downloading ${release.title}`}
                          disabled={busy !== ''}
                          onClick={() => void downloadRelease(release)}
                        >
                          Download anyway
                        </Button>
                        <Button size="xs" variant="ghost" onClick={() => setConfirmRelease('')}>
                          Cancel
                        </Button>
                      </p>
                    ) : null}
                  </div>
                  <Button
                    size="xs"
                    variant={release.decision.allowed ? 'default' : 'outline'}
                    disabled={busy !== '' || !canWrite || downloading}
                    title={
                      !canWrite
                        ? 'Downloading an album release requires the library.write permission'
                        : downloading
                          ? 'This album already has an active download.'
                          : undefined
                    }
                    aria-label={
                      release.decision.allowed
                        ? `Download ${release.title}`
                        : `Download rejected release ${release.title} anyway`
                    }
                    onClick={() =>
                      release.decision.allowed ? void downloadRelease(release) : setConfirmRelease(release.id)
                    }
                  >
                    <DownloadIcon /> {release.decision.allowed ? 'Download' : 'Download anyway'}
                  </Button>
                </li>
              ))}
            </ul>
          </section>
        ) : null}

        <section className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">Tracks and files</h3>
          {tracks.length === 0 && files.length === 0 ? (
            <p className="text-xs text-muted-foreground">No tracklist yet. Refresh metadata or import a release.</p>
          ) : null}
          <ul className="flex flex-col gap-1">
            {(tracks.length > 0
              ? tracks.map((track) => ({
                  key: track.id,
                  label: `${track.disc > 1 ? `${track.disc}-` : ''}${String(track.number).padStart(2, '0')} ${track.title}`,
                  detail: track.file
                    ? `${track.file.format?.toUpperCase() || ''} ${track.file.bitrateKbps ? `${track.file.bitrateKbps} kbps` : ''}`.trim()
                    : '',
                  path: track.file?.path ?? '',
                  missing: track.missing || !track.file,
                }))
              : files.map((file) => ({
                  key: `${file.path}`,
                  label: file.trackTitle || fileName(file.path),
                  detail: `${file.format?.toUpperCase() || ''} ${file.bitrateKbps ? `${file.bitrateKbps} kbps` : ''}`.trim(),
                  path: file.path,
                  missing: file.missing ?? false,
                }))
            ).map((row) => (
              <li key={row.key} className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1 text-xs">
                <span className="min-w-0 truncate" title={row.path || undefined}>{row.label}</span>
                <span className={row.missing ? 'text-amber-300' : 'text-muted-foreground'}>
                  {row.missing ? 'No file yet' : row.detail || 'Downloaded'}
                </span>
              </li>
            ))}
          </ul>
          {(tracks.length > 0 ? tracks.filter((track) => track.file) : files.filter((file) => !file.missing)).length > 0 ? (
            <details>
              <summary className="cursor-pointer text-xs text-muted-foreground">File paths</summary>
              <ul className="mt-1 flex flex-col gap-1 text-[11px] break-all text-muted-foreground">
                {(tracks.length > 0
                  ? tracks.filter((track) => track.file).map((track) => ({ key: track.id, path: track.file?.path ?? '' }))
                  : files.filter((file) => !file.missing).map((file) => ({ key: file.path, path: file.path }))
                ).map((row) => (
                  <li key={`path-${row.key}`}>{row.path}</li>
                ))}
              </ul>
            </details>
          ) : null}
        </section>

        {history.length > 0 ? (
          <details className="rounded-md border border-border p-2">
            <summary className="cursor-pointer text-sm font-medium">History ({history.length})</summary>
            <div className="mt-1 flex flex-col gap-1">
              {history.slice(0, 8).map((entry) => (
                <p key={entry.id} className="text-[11px] text-muted-foreground">
                  {new Date(entry.createdAt).toLocaleString()} · {entry.type} · {entry.message}
                </p>
              ))}
            </div>
          </details>
        ) : null}
      </div>
    </Modal>
  )
}

function DiscoverDialog({
  config,
  knownArtists,
  canWrite,
  onClose,
  onChanged,
}: {
  config: MusicConfig
  knownArtists: Artist[]
  canWrite: boolean
  onClose: () => void
  onChanged: () => void
}) {
  const [query, setQuery] = useState('')
  const [artists, setArtists] = useState<ArtistResult[]>([])
  const [albums, setAlbums] = useState<AlbumResult[]>([])
  const [monitored, setMonitored] = useState(false)
  const [monitorOption, setMonitorOption] = useState('all')
  const [profileId, setProfileId] = useState(config.defaultProfileId)
  const [rootId, setRootId] = useState(config.rootFolders[0]?.id ?? '')
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [busy, setBusy] = useState('')

  const markArtist = (musicBrainzId: string) => {
    setArtists((current) => current.map((entry) => (entry.musicBrainzId === musicBrainzId ? { ...entry, inLibrary: true } : entry)))
  }

  const markAlbum = (musicBrainzId: string) => {
    setAlbums((current) => current.map((entry) => (entry.musicBrainzId === musicBrainzId ? { ...entry, inLibrary: true } : entry)))
  }

  const search = async () => {
    if (!query.trim()) return
    setBusy('search')
    setError('')
    setSuccess('')
    try {
      const result = await musicApi.discover(query.trim())
      setArtists(result.artists ?? [])
      setAlbums(result.albums ?? [])
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const addArtist = async (artist: ArtistResult) => {
    if (artist.inLibrary || busy !== '') return
    setBusy(`artist:${artist.musicBrainzId}`)
    setError('')
    setSuccess('')
    try {
      await musicApi.addArtist({ musicBrainzId: artist.musicBrainzId, name: artist.name, monitored, monitorOption, profileId, rootId } satisfies AddArtistInput)
      markArtist(artist.musicBrainzId)
      setSuccess(`Added ${artist.name}${monitored ? ' with automatic downloads on.' : ' to Wanted. Choose a release when you’re ready.'}`)
      onChanged()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const addAlbum = async (result: AlbumResult) => {
    if (result.inLibrary || busy !== '') return
    setBusy(`album:${result.musicBrainzId}`)
    setError('')
    setSuccess('')
    try {
      // Album-only adds leave the rest of a new artist’s discography unselected.
      const existing = knownArtists.find((artist) => artist.musicBrainzId === result.artistId)
      const artist =
        existing ??
        (await musicApi.addArtist({ musicBrainzId: result.artistId, name: result.artistName, monitored, monitorOption: 'none', profileId, rootId }))
      await musicApi.addAlbum({ artistId: artist.id, musicBrainzId: result.musicBrainzId, title: result.title, year: result.year, monitored, profileId, rootId })
      markAlbum(result.musicBrainzId)
      markArtist(result.artistId)
      setSuccess(`Added ${result.title}${monitored ? ' with automatic downloads on.' : ' to Wanted. Choose a release when you’re ready.'}`)
      onChanged()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  return (
    <Modal title="Discover music" description="Find an artist or album to add to your library." onClose={onClose} wide>
      <div className="flex flex-col gap-4">
        <ErrorNotice message={error} />
        <div className="flex gap-2">
          <Input
            autoFocus
            value={query}
            placeholder="Artist or album"
            aria-label="Search MusicBrainz"
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => event.key === 'Enter' && void search()}
          />
          <Button onClick={() => void search()} disabled={busy !== ''}>
            {busy === 'search' ? <LoaderCircleIcon className="animate-spin" /> : <SearchIcon />} Search
          </Button>
        </div>
        <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={monitored} onChange={(event) => setMonitored(event.target.checked)} />
            Download automatically
          </label>
          {monitored && <label className="flex items-center gap-2">
            Releases to download
            <select className="h-8 rounded-md border border-input bg-background px-2" value={monitorOption} onChange={(event) => setMonitorOption(event.target.value)} aria-label="Releases to download">
              {monitorOptions.map((option) => (
                <option key={option.value} value={option.value}>{option.label}</option>
              ))}
            </select>
          </label>}
          <label className="flex items-center gap-2">
            Quality profile
            <select className="h-8 rounded-md border border-input bg-background px-2" value={profileId} onChange={(event) => setProfileId(event.target.value)} aria-label="Quality profile">
              {config.qualityProfiles.map((profile) => (
                <option key={profile.id} value={profile.id}>{profile.name}</option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-2">
            Root folder
            <select className="h-8 rounded-md border border-input bg-background px-2" value={rootId} onChange={(event) => setRootId(event.target.value)} aria-label="Root folder">
              {config.rootFolders.map((root) => (
                <option key={root.id} value={root.id}>{root.path}</option>
              ))}
            </select>
          </label>
        </div>
        <p className="text-[11px] text-muted-foreground" role="status">
          {monitored
            ? `Added entries are searched and downloaded automatically (${monitorOptionLabels[monitorOption] ?? monitorOption}) when a release matches.`
            : 'Added entries stay in Wanted with automatic downloads off until you choose a release.'}
        </p>
        <p
          role="status"
          aria-live="polite"
          className={
            success
              ? 'flex items-center gap-2 rounded-md border border-emerald-400/30 bg-emerald-400/10 p-2 text-xs text-emerald-300'
              : 'sr-only'
          }
        >
          {success ? <CheckIcon className="size-3.5 shrink-0" /> : null}
          {success}
        </p>
        <section className="flex flex-col gap-1">
          <h3 className="text-sm font-medium">Artists</h3>
          {artists.length === 0 ? <p className="text-xs text-muted-foreground">No artist results yet.</p> : null}
          {artists.map((artist) => (
            <div key={artist.musicBrainzId} className="flex items-center justify-between gap-2 rounded-md border border-border p-2">
              <div className="min-w-0">
                <p className="truncate text-xs font-medium">{artist.name}</p>
                <p className="text-[11px] text-muted-foreground">{[artist.type, artist.country, artist.disambiguation].filter(Boolean).join(' · ')}</p>
              </div>
              <Button
                size="xs"
                disabled={busy !== '' || !canWrite || artist.inLibrary}
                aria-label={artist.inLibrary ? `${artist.name} is already in the library` : `Add ${artist.name} to the library`}
                onClick={() => void addArtist(artist)}
              >
                {busy === `artist:${artist.musicBrainzId}` ? <LoaderCircleIcon className="animate-spin" /> : artist.inLibrary ? <CheckIcon /> : <PlusIcon />}
                {artist.inLibrary ? 'Added' : 'Add'}
              </Button>
            </div>
          ))}
        </section>
        <section className="flex flex-col gap-1">
          <h3 className="text-sm font-medium">Albums</h3>
          <p className="text-[11px] text-muted-foreground">
            Add individual albums or an artist’s full catalog.
          </p>
          {albums.length === 0 ? <p className="text-xs text-muted-foreground">No album results yet.</p> : null}
          {albums.map((album) => (
            <div key={album.musicBrainzId} className="flex items-center justify-between gap-2 rounded-md border border-border p-2">
              <div className="min-w-0">
                <p className="truncate text-xs font-medium">{album.title}</p>
                <p className="text-[11px] text-muted-foreground">
                  {[album.artistName, album.year || '', album.type].filter(Boolean).join(' · ')}
                </p>
              </div>
              <Button
                size="xs"
                disabled={busy !== '' || !canWrite || album.inLibrary}
                aria-label={album.inLibrary ? `${album.title} is already in the library` : `Add ${album.title} to the library`}
                onClick={() => void addAlbum(album)}
              >
                {busy === `album:${album.musicBrainzId}` ? <LoaderCircleIcon className="animate-spin" /> : album.inLibrary ? <CheckIcon /> : <PlusIcon />}
                {album.inLibrary ? 'Added' : 'Add'}
              </Button>
            </div>
          ))}
        </section>
      </div>
    </Modal>
  )
}

function ScanDialog({
  config,
  canWrite,
  onClose,
  onChanged,
}: {
  config: MusicConfig
  canWrite: boolean
  onClose: () => void
  onChanged: () => void
}) {
  const [rootId, setRootId] = useState(config.rootFolders[0]?.id ?? '')
  const [candidates, setCandidates] = useState<Candidate[] | null>(null)
  const [imported, setImported] = useState<Record<string, string>>({})
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [busy, setBusy] = useState('')

  const scan = async () => {
    setBusy('scan')
    setError('')
    setSuccess('')
    try {
      setCandidates(await musicApi.scan(rootId))
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const importCandidate = async (candidate: Candidate) => {
    setBusy(candidate.path)
    setError('')
    setSuccess('')
    try {
      const root = config.rootFolders.find((item) => item.id === rootId)
      const path = root ? `${root.path.replace(/\/+$/, '')}/${candidate.path}` : candidate.path
      const album = await musicApi.importPath({ path, albumId: candidate.matchedAlbumId })
      setImported((current) => ({ ...current, [candidate.path]: album.title || 'the matched album' }))
      setSuccess(`Imported ${candidate.album || candidate.path} into ${album.title || 'the matched album'}.`)
      onChanged()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  return (
    <Modal
      title="Scan library"
      description="Match folders in a music root against the catalog. Scanning is read-only and nothing imports until you press Import."
      onClose={onClose}
      wide
    >
      <div className="flex flex-col gap-4">
        <ErrorNotice message={error} />
        <SuccessNotice message={success} />
        <div className="flex flex-wrap items-center gap-2">
          <select
            className="h-8 rounded-md border border-input bg-background px-2 text-xs"
            value={rootId}
            aria-label="Music root folder"
            onChange={(event) => setRootId(event.target.value)}
          >
            {config.rootFolders.length === 0 && <option value="">No music root folders configured</option>}
            {config.rootFolders.map((root) => (
              <option key={root.id} value={root.id}>{root.path}</option>
            ))}
          </select>
          <Button size="sm" disabled={busy !== '' || !canWrite || !rootId} onClick={() => void scan()}>
            {busy === 'scan' ? <LoaderCircleIcon className="animate-spin" /> : <FolderSearchIcon />}
            {candidates === null ? 'Scan folder' : 'Scan again'}
          </Button>
        </div>
        {candidates ? (
          candidates.length === 0 ? (
            <p className="text-xs text-muted-foreground">No album folders found in this root.</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {candidates.map((candidate) => (
                <li key={candidate.path} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-2">
                  <div className="min-w-0">
                    <p className="truncate text-xs font-medium">{candidate.album || candidate.path}</p>
                    <p className="text-[11px] text-muted-foreground">
                      {[candidate.artist, candidate.year || '', `${candidate.tracks} files`, `${candidate.discs} disc(s)`, candidate.format?.toUpperCase()].filter(Boolean).join(' · ')}
                      {candidate.size ? ` · ${formatBytes(candidate.size)}` : ''}
                    </p>
                    {candidate.error ? <p className="text-[11px] text-amber-300">{candidate.error}</p> : null}
                    {imported[candidate.path] ? (
                      <p className="text-[11px] text-emerald-300">Imported into {imported[candidate.path]}</p>
                    ) : !candidate.matchedAlbumId ? (
                      <p className="text-[11px] text-amber-300">
                        No catalog match. Add the album first, then scan again to import these files.
                      </p>
                    ) : null}
                  </div>
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={busy !== '' || !canWrite || !candidate.matchedAlbumId || Boolean(imported[candidate.path])}
                    aria-label={
                      candidate.matchedAlbumId
                        ? `Import ${candidate.album || candidate.path} into the matched album`
                        : `${candidate.album || candidate.path} has no matched album to import into`
                    }
                    onClick={() => void importCandidate(candidate)}
                  >
                    {busy === candidate.path ? <LoaderCircleIcon className="animate-spin" /> : <DownloadIcon />}
                    {imported[candidate.path] ? 'Imported' : 'Import'}
                  </Button>
                </li>
              ))}
            </ul>
          )
        ) : (
          <p className="text-xs text-muted-foreground">
            Choose a root folder and scan to list album folders that are not in the library yet. Folders matched to a
            catalog album can be imported; unmatched folders need the album added first.
          </p>
        )}
      </div>
    </Modal>
  )
}

function ProfileEditor({ profile, onChange, onRemove }: { profile: QualityProfile; onChange: (next: QualityProfile) => void; onRemove: () => void }) {
  return (
    <div className="flex flex-col gap-2 rounded-md border border-border p-2">
      <div className="flex flex-wrap items-center gap-2">
        <Input className="h-8 max-w-40" value={profile.name} onChange={(event) => onChange({ ...profile, name: event.target.value })} placeholder="Name" />
        <Input className="h-8 max-w-32" value={profile.cutoff} onChange={(event) => onChange({ ...profile, cutoff: event.target.value })} placeholder="Cutoff" />
        <Input
          className="h-8 max-w-28"
          type="number"
          min={0}
          value={profile.minBitrateKbps}
          onChange={(event) => onChange({ ...profile, minBitrateKbps: Number(event.target.value) })}
          placeholder="kbps"
        />
        <label className="flex items-center gap-1 text-xs">
          <input type="checkbox" checked={profile.losslessOnly} onChange={(event) => onChange({ ...profile, losslessOnly: event.target.checked })} /> Lossless only
        </label>
        <label className="flex items-center gap-1 text-xs" title="Download a better release when one matches this profile.">
          <input type="checkbox" checked={profile.upgrade} onChange={(event) => onChange({ ...profile, upgrade: event.target.checked })} /> Allow upgrades
        </label>
        <Button size="icon-xs" variant="ghost" onClick={onRemove} aria-label={`Remove quality profile ${profile.name || 'without a name'}`}>
          <Trash2Icon />
        </Button>
      </div>
      <div className="flex flex-wrap gap-1">
        {knownFormats.map((format) => {
          const active = profile.formats.includes(format)
          return (
            <button
              key={format}
              type="button"
              className={`rounded-md border px-2 py-0.5 text-[11px] ${active ? 'border-primary/40 bg-primary/10 text-primary' : 'border-border text-muted-foreground'}`}
              onClick={() =>
                onChange({
                  ...profile,
                  formats: active ? profile.formats.filter((item) => item !== format) : [...profile.formats, format],
                })
              }
            >
              {format.toUpperCase()}
            </button>
          )
        })}
      </div>
    </div>
  )
}

function SettingsPanel({
  config,
  canWrite,
  section = 'all',
  onSaved,
}: {
  config: MusicConfig
  canWrite: boolean
  section?: 'all' | 'connections' | 'storage'
  onSaved: () => void
}) {
  const [draft, setDraft] = useState<MusicConfig>(config)
  const [tests, setTests] = useState<MusicConnectionTests | null>(null)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [busy, setBusy] = useState('')
  const [advancedOpen, setAdvancedOpen] = useState(false)
  // Polling reloads the stored config; a draft with unsaved edits is never overwritten by a refresh.
  const baseline = useRef(config)

  useEffect(() => {
    const previous = baseline.current
    baseline.current = config
    setDraft((current) => (JSON.stringify(current) === JSON.stringify(previous) ? config : current))
  }, [config])

  const dirty = JSON.stringify(draft) !== JSON.stringify(config)

  const save = async () => {
    setBusy('save')
    setError('')
    setSuccess('')
    try {
      await musicApi.saveConfig(draft)
      setSuccess('Music settings saved. New imports and scheduled searches use this configuration.')
      onSaved()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const test = async () => {
    setBusy('test')
    setError('')
    setSuccess('')
    try {
      setTests(await musicApi.testConfig())
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const showStorage = section !== 'connections'
  const showConnections = section !== 'storage'
  return (
    <div className="flex flex-col gap-4">
      <ErrorNotice message={error} />
      <SuccessNotice message={success} />

      {showConnections ? (
        <Card>
          <CardHeader>
            <CardTitle>Metadata provider</CardTitle>
            <CardDescription>Source of artist, album, and cover art metadata for the whole library.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3 md:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              MusicBrainz API URL
              <Input value={draft.musicBrainzURL} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, musicBrainzURL: event.target.value })} />
            </label>
            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              MusicBrainz rate limit (ms)
              <Input type="number" min={0} value={draft.musicBrainzRateMs} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, musicBrainzRateMs: Number(event.target.value) })} />
            </label>
            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              Cover Art Archive URL (empty disables)
              <Input value={draft.coverArtURL} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, coverArtURL: event.target.value })} />
            </label>
            <p className="flex items-end text-xs text-muted-foreground">
              ffprobe is {config.ffprobeAvailable ? 'available' : 'not found'} for reading imported files.
            </p>
            {tests ? (
              <p role="status" className="text-xs text-muted-foreground md:col-span-2">
                MusicBrainz: {tests.musicBrainz.ok ? 'ok' : `failed (${tests.musicBrainz.error})`} · Indexer:{' '}
                {tests.indexer.ok ? 'ok' : `failed (${tests.indexer.error})`} · ffprobe:{' '}
                {tests.ffprobe.ok ? 'ok' : `failed (${tests.ffprobe.error})`}
              </p>
            ) : null}
          </CardContent>
        </Card>
      ) : null}

      {showStorage ? (
        <Card>
          <CardHeader>
            <CardTitle>Library folders</CardTitle>
            <CardDescription>Where imported music is organized on your server.</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {draft.rootFolders.length === 0 && (
              <p className="rounded-md border border-dashed border-border px-3 py-4 text-sm text-muted-foreground">
                No music root folders yet. Add the folder that holds your music.
              </p>
            )}
            {draft.rootFolders.map((root, index) => (
              <div key={`${root.id || 'new'}-${index}`} className="flex flex-wrap gap-2">
                {advancedOpen ? (
                  <Input
                    className="max-w-40"
                    value={root.id}
                    placeholder="ID"
                    aria-label={`Music root folder ${index + 1} ID`}
                    disabled={!canWrite}
                    onChange={(event) => {
                      const roots = [...draft.rootFolders]
                      roots[index] = { ...root, id: event.target.value }
                      setDraft({ ...draft, rootFolders: roots })
                    }}
                  />
                ) : null}
                <Input
                  className="min-w-56 flex-1"
                  value={root.path}
                  placeholder="/music"
                  aria-label={`Music root folder ${index + 1} path`}
                  disabled={!canWrite}
                  onChange={(event) => {
                    const roots = [...draft.rootFolders]
                    roots[index] = { ...root, path: event.target.value }
                    setDraft({ ...draft, rootFolders: roots })
                  }}
                />
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label={`Remove root folder ${root.path || index + 1}`}
                  disabled={!canWrite}
                  onClick={() => setDraft({ ...draft, rootFolders: draft.rootFolders.filter((_, i) => i !== index) })}
                >
                  <Trash2Icon />
                </Button>
              </div>
            ))}
            <Button size="sm" variant="outline" disabled={!canWrite} onClick={() => setDraft({ ...draft, rootFolders: [...draft.rootFolders, { id: `music${draft.rootFolders.length + 1}`, path: '' }] })}>
              <PlusIcon /> Add root
            </Button>
            <p className="text-[11px] text-muted-foreground">
              Removing a folder only unregisters it here; files on disk are never moved or deleted by saving settings.
            </p>
          </CardContent>
        </Card>
      ) : null}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <Button
          size="sm"
          variant="ghost"
          aria-expanded={advancedOpen}
          aria-controls="music-advanced-settings"
          onClick={() => setAdvancedOpen((current) => !current)}
        >
          <SettingsIcon className="size-3.5" />
          {advancedOpen ? 'Hide advanced options' : 'Advanced options'}
        </Button>
        <p className="text-xs text-muted-foreground">
          Naming templates, folder IDs, import mode, scan intervals, and quality profiles.
        </p>
      </div>

      {advancedOpen ? (
        <div id="music-advanced-settings" className="flex flex-col gap-4">
          {showStorage ? (
            <Card>
              <CardHeader>
                <CardTitle>Naming and imports</CardTitle>
                <CardDescription>How folders and files are named, and how files are copied into the library.</CardDescription>
              </CardHeader>
              <CardContent className="grid gap-3 md:grid-cols-2">
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Folder template
                  <Input value={draft.folderTemplate} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, folderTemplate: event.target.value })} />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  File template
                  <Input value={draft.fileTemplate} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, fileTemplate: event.target.value })} />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Import mode
                  <select className="h-9 rounded-md border border-input bg-background px-2 text-sm" value={draft.importMode} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, importMode: event.target.value })}>
                    {importModes.map((mode) => (
                      <option key={mode} value={mode}>{mode}</option>
                    ))}
                  </select>
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  ffprobe path {config.ffprobeAvailable ? '(available)' : '(not found)'}
                  <Input value={draft.ffprobePath} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, ffprobePath: event.target.value })} />
                </label>
                <p className="text-[11px] text-muted-foreground md:col-span-2">
                  Torrent imports always hardlink so the payload keeps seeding, even in move mode.
                </p>
              </CardContent>
            </Card>
          ) : null}

          {showConnections ? (
            <Card>
              <CardHeader>
                <CardTitle>Automation</CardTitle>
                <CardDescription>How often indexers are checked for albums with automatic downloads on.</CardDescription>
              </CardHeader>
              <CardContent className="grid gap-3 md:grid-cols-2">
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Check indexer feeds every (minutes)
                  <Input type="number" min={1} value={draft.pollMinutes} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, pollMinutes: Number(event.target.value) })} />
                </label>
                <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                  Search wanted albums every (hours)
                  <Input type="number" min={1} value={draft.searchHours} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, searchHours: Number(event.target.value) })} />
                </label>
                <label className="flex items-center gap-2 text-xs text-muted-foreground md:col-span-2">
                  <input type="checkbox" checked={draft.retryFailed} disabled={!canWrite} onChange={(event) => setDraft({ ...draft, retryFailed: event.target.checked })} />
                  Retry failed releases with another candidate
                </label>
              </CardContent>
            </Card>
          ) : null}

          {showConnections ? (
            <Card>
              <CardHeader>
                <CardTitle>Quality profiles</CardTitle>
                <CardDescription>Format preference, lossless and bitrate floors, upgrades, and cutoff behaviour.</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                {draft.qualityProfiles.map((profile, index) => (
                  <ProfileEditor
                    key={profile.id}
                    profile={profile}
                    onChange={(next) => {
                      const profiles = [...draft.qualityProfiles]
                      profiles[index] = next
                      setDraft({ ...draft, qualityProfiles: profiles })
                    }}
                    onRemove={() => setDraft({ ...draft, qualityProfiles: draft.qualityProfiles.filter((_, i) => i !== index) })}
                  />
                ))}
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!canWrite}
                  onClick={() =>
                    setDraft({
                      ...draft,
                      qualityProfiles: [
                        ...draft.qualityProfiles,
                        { id: `profile${draft.qualityProfiles.length + 1}`, name: 'New profile', formats: ['flac', 'mp3'], losslessOnly: false, minBitrateKbps: 0, minMB: 0, maxMB: 0, cutoff: '', upgrade: false },
                      ],
                    })
                  }
                >
                  <PlusIcon /> Add profile
                </Button>
                <label className="flex items-center gap-2 text-xs text-muted-foreground">
                  Default profile
                  <select
                    className="h-8 rounded-md border border-input bg-background px-2"
                    value={draft.defaultProfileId}
                    disabled={!canWrite}
                    aria-label="Default quality profile"
                    onChange={(event) => setDraft({ ...draft, defaultProfileId: event.target.value })}
                  >
                    {draft.qualityProfiles.map((profile) => (
                      <option key={profile.id} value={profile.id}>{profile.name}</option>
                    ))}
                  </select>
                </label>
              </CardContent>
            </Card>
          ) : null}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-2 border-t border-border pt-4">
        <Button onClick={() => void save()} disabled={busy !== '' || !canWrite || !dirty}>
          {busy === 'save' ? <LoaderCircleIcon className="animate-spin" /> : <SaveIcon className="size-4" />} Save settings
        </Button>
        <Button
          variant="outline"
          onClick={() => {
            setDraft(config)
            setError('')
            setSuccess('')
          }}
          disabled={!dirty || busy !== ''}
        >
          Discard changes
        </Button>
        {showConnections ? (
          <Button variant="outline" onClick={() => void test()} disabled={busy !== '' || !canWrite}>
            Test connections
          </Button>
        ) : null}
        <span className="text-xs text-muted-foreground" role="status">
          {dirty ? 'Unsaved changes.' : 'Settings are saved.'}
        </span>
        {!canWrite ? <span className="text-xs text-muted-foreground">Read-only: saving configuration needs settings.write.</span> : null}
      </div>
    </div>
  )
}

// MusicSettings is the drop-in music settings card for the Storage and Connections pages.
export function MusicSettings({ section = 'all' }: { section?: 'all' | 'connections' | 'storage' }) {
  const { can } = useAuth()
  const canRead = can(accessPermissions.settingsRead)
  const canWrite = can(accessPermissions.settingsWrite)
  const [config, setConfig] = useState<MusicConfig | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const cfg = await musicApi.config(signal)
      if (signal?.aborted) return
      setConfig(cfg)
      setError('')
    } catch (cause) {
      if (signal?.aborted) return
      setError(notice(cause))
    }
  }, [])

  useEffect(() => {
    if (!canRead) return
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is applied only after the request settles
    void load(controller.signal)
    return () => controller.abort()
  }, [canRead, load])

  if (!canRead) return null
  return (
    <div className="space-y-5">
      <ErrorNotice message={error} />
      {config ? (
        <SettingsPanel section={section} config={config} canWrite={canWrite} onSaved={() => void load()} />
      ) : (
        <p className="text-sm text-muted-foreground">Loading music settings…</p>
      )}
    </div>
  )
}

export function MusicPage({ active = true }: { active?: boolean }) {
  const routeActive = useMusicRoute()
  const { can } = useAuth()
  const canRead = can(accessPermissions.libraryRead)
  const canWrite = can(accessPermissions.libraryWrite)
  const canReadSettings = can(accessPermissions.settingsRead)
  const canWriteSettings = can(accessPermissions.settingsWrite)
  const [tab, setTab] = useState<Tab>('library')
  const [artists, setArtists] = useState<Artist[]>([])
  const [albums, setAlbums] = useState<Album[]>([])
  const [wanted, setWanted] = useState<Album[]>([])
  const [calendar, setCalendar] = useState<{ id: string; albumId: string; title: string; artistName: string; releaseDate: string }[]>([])
  const [history, setHistory] = useState<HistoryEntry[]>([])
  const [config, setConfig] = useState<MusicConfig | null>(null)
  const [openAlbum, setOpenAlbum] = useState<string | null>(null)
  const [dialog, setDialog] = useState<'discover' | 'scan' | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [success, setSuccess] = useState('')
  const [expandedArtist, setExpandedArtist] = useState<string | null>(null)
  const [loadedTabs, setLoadedTabs] = useState<Partial<Record<Tab, boolean>>>({})

  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        // Configuration is a settings.read resource; browsing only needs library.read.
        const [artistList, albumList, cfg] = await Promise.all([
          musicApi.artists(signal),
          musicApi.albums(signal),
          canReadSettings ? musicApi.config(signal) : Promise.resolve(null),
        ])
        if (signal?.aborted) return
        setArtists(artistList)
        setAlbums(albumList)
        if (cfg) setConfig(cfg)
        setError('')
      } catch (cause) {
        if (signal?.aborted) return
        setError(notice(cause))
      }
    },
    [canReadSettings],
  )

  const loadTab = useCallback(async (next: Tab, signal?: AbortSignal) => {
    try {
      if (next === 'wanted') setWanted(await musicApi.wanted(signal))
      if (next === 'calendar') setCalendar(await musicApi.calendar(signal))
      if (next === 'history') setHistory(await musicApi.history(signal))
      if (!signal?.aborted) setLoadedTabs((current) => ({ ...current, [next]: true }))
    } catch (cause) {
      if (signal?.aborted) return
      setError(notice(cause))
    }
  }, [])

  const poll = useCallback((signal: AbortSignal) => Promise.all([load(signal), loadTab(tab, signal)]), [load, loadTab, tab])
  useMusicPoll(poll, active && routeActive && canRead)

  const refresh = useCallback(() => {
    void load()
    void loadTab(tab)
  }, [load, loadTab, tab])

  const sync = async () => {
    setBusy('sync')
    setError('')
    setSuccess('')
    try {
      const result = await musicApi.sync(true)
      setSuccess(
        `Searched ${result.searched}, queued ${result.queued}, imported ${result.imported}, refreshed ${result.refreshed}.`,
      )
      refresh()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const toggleArtist = async (artist: Artist) => {
    setBusy(artist.id)
    setError('')
    try {
      await musicApi.monitorArtist(artist.id, !artist.monitored, artist.monitorOption)
      setSuccess(
        artist.monitored
          ? `Automatic downloads turned off for ${artist.name}.`
          : `Automatic downloads turned on for ${artist.name}.`,
      )
      refresh()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const refreshArtist = async (artist: Artist) => {
    setBusy(artist.id)
    setError('')
    try {
      await musicApi.refreshArtist(artist.id)
      setSuccess(`Metadata refreshed for ${artist.name}.`)
      refresh()
    } catch (cause) {
      setError(notice(cause))
    } finally {
      setBusy('')
    }
  }

  const albumGroups = useMemo(() => {
    const groups = new Map<string, Album[]>()
    for (const album of albums) {
      const list = groups.get(album.artistId) ?? []
      list.push(album)
      groups.set(album.artistId, list)
    }
    return groups
  }, [albums])

  if (!canRead) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeading title="Music" description="Artists, albums, and automatic downloads." />
        <Card>
          <CardContent className="items-start gap-2">
            <p className="text-sm text-muted-foreground">Your account cannot read the music library.</p>
          </CardContent>
        </Card>
      </div>
    )
  }

  if (dialog && config) {
    if (dialog === 'discover') {
      return <DiscoverDialog config={config} knownArtists={artists} canWrite={canWrite} onClose={() => setDialog(null)} onChanged={refresh} />
    }
    return <ScanDialog config={config} canWrite={canWrite} onClose={() => setDialog(null)} onChanged={refresh} />
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeading
        title="Music"
        description="Artists, albums, and automatic downloads."
        action={
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={() => setDialog('discover')} disabled={!canWrite || !canReadSettings || !config}>
              <SearchIcon /> Discover
            </Button>
            <Button size="sm" variant="outline" onClick={() => setDialog('scan')} disabled={!canWrite || !canReadSettings || !config}>
              <FolderSearchIcon /> Scan library
            </Button>
            <Button size="sm" variant="outline" onClick={() => void sync()} disabled={busy !== '' || !canWrite}>
              {busy === 'sync' ? <LoaderCircleIcon className="animate-spin" /> : <RefreshCwIcon />} Sync now
            </Button>
          </div>
        }
      />

      <div className="flex flex-wrap gap-1" role="tablist" aria-label="Music sections">
        {tabs
          .filter((item) => item !== 'settings' || canReadSettings)
          .map((item) => (
            <Button key={item} role="tab" aria-selected={tab === item} size="sm" variant={tab === item ? 'default' : 'ghost'} onClick={() => setTab(item)}>
              {item === 'library' ? <ListMusicIcon /> : null}
              {item === 'wanted' ? <MusicIcon /> : null}
              {item === 'calendar' ? <CalendarIcon /> : null}
              {item === 'history' ? <HistoryIcon /> : null}
              {item === 'settings' ? <SettingsIcon /> : null}
              {tabLabels[item]}
            </Button>
          ))}
      </div>

      <ErrorNotice message={error} />
      <SuccessNotice message={success} />

      {tab === 'library' ? (
        <div className="flex flex-col gap-3">
          {artists.length === 0 ? (
            <Card>
              <CardContent className="items-start gap-2">
                <p className="text-sm text-muted-foreground">
                  No artists yet. Use Discover to add an artist or an album; automatic downloads stay off unless you
                  turn them on.
                </p>
              </CardContent>
            </Card>
          ) : null}
          {artists.map((artist) => {
            const artistAlbums = albumGroups.get(artist.id) ?? []
            const expanded = expandedArtist === artist.id
            return (
              <Card key={artist.id}>
                <CardHeader>
                  <div className="flex flex-wrap items-center gap-2">
                    <Disc3Icon className="size-4 text-muted-foreground" />
                    <CardTitle className="text-sm">{artist.name}</CardTitle>
                    <StatusBadge status={artist.status} />
                    <Badge variant="outline">
                      {artist.monitored
                        ? `Automatic downloads on · ${monitorOptionLabels[artist.monitorOption] ?? artist.monitorOption}`
                        : 'Automatic downloads off'}
                    </Badge>
                    <span className="text-xs text-muted-foreground">{artistAlbums.length} albums</span>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      size="xs"
                      variant="outline"
                      aria-pressed={artist.monitored}
                      disabled={busy !== '' || !canWrite}
                      title={
                        artist.monitored
                          ? `Stop searching and downloading ${artist.name} automatically.`
                          : `Search indexers and download ${artist.name} releases automatically.`
                      }
                      onClick={() => void toggleArtist(artist)}
                    >
                      <DownloadIcon /> Download automatically
                    </Button>
                    <Button size="xs" variant="outline" disabled={busy !== '' || !canWrite || !artist.musicBrainzId} onClick={() => void refreshArtist(artist)}>
                      <RefreshCwIcon /> Refresh metadata
                    </Button>
                    <Button
                      size="xs"
                      variant="ghost"
                      aria-expanded={expanded}
                      aria-controls={`artist-albums-${artist.id}`}
                      onClick={() => setExpandedArtist(expanded ? null : artist.id)}
                    >
                      {expanded ? 'Hide albums' : 'Show albums'}
                    </Button>
                  </div>
                </CardHeader>
                {expanded ? (
                  <CardContent id={`artist-albums-${artist.id}`} className="flex flex-col gap-1">
                    {artistAlbums.length === 0 ? <p className="text-xs text-muted-foreground">No albums in the library yet.</p> : null}
                    {artistAlbums.map((album) => (
                      <button
                        key={album.id}
                        type="button"
                        className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border px-2 py-1 text-left text-xs hover:bg-muted/50"
                        aria-label={`Open ${album.title}${album.year ? ` (${album.year})` : ''}`}
                        onClick={() => setOpenAlbum(album.id)}
                      >
                        <span className="min-w-0 truncate">
                          {album.title}
                          {album.year ? ` (${album.year})` : ''}
                        </span>
                        <span className="flex items-center gap-2">
                          {album.format ? <Badge variant="outline">{album.format.toUpperCase()}</Badge> : null}
                          <StatusBadge status={album.status} />
                        </span>
                      </button>
                    ))}
                  </CardContent>
                ) : null}
              </Card>
            )
          })}
        </div>
      ) : null}

      {tab === 'wanted' ? (
        <Card>
          <CardHeader>
            <CardTitle>Wanted albums</CardTitle>
            <CardDescription>
              Albums with automatic downloads on that are missing files or below their profile cutoff.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-1">
            {!loadedTabs.wanted ? (
              <p className="text-xs text-muted-foreground" role="status">Loading wanted albums…</p>
            ) : wanted.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                Nothing is waiting. Turn on automatic downloads for an artist or album to fill this list.
              </p>
            ) : null}
            {wanted.map((album) => (
              <button
                key={album.id}
                type="button"
                className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border px-2 py-1 text-left text-xs hover:bg-muted/50"
                aria-label={`Open ${album.artistName} ${album.title}`}
                onClick={() => setOpenAlbum(album.id)}
              >
                <span className="min-w-0 truncate">{album.artistName} · {album.title}</span>
                <span className="flex items-center gap-2">
                  {album.lastSearchAt ? <span className="text-muted-foreground">searched {formatAge(album.lastSearchAt)}</span> : null}
                  <StatusBadge status={album.status} />
                </span>
              </button>
            ))}
          </CardContent>
        </Card>
      ) : null}

      {tab === 'calendar' ? (
        <Card>
          <CardHeader>
            <CardTitle>Release calendar</CardTitle>
            <CardDescription>Album releases around today.</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-1">
            {!loadedTabs.calendar ? (
              <p className="text-xs text-muted-foreground" role="status">Loading release dates…</p>
            ) : calendar.length === 0 ? (
              <p className="text-xs text-muted-foreground">No dated releases in the window.</p>
            ) : null}
            {calendar.map((entry) => (
              <div key={entry.id} className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1 text-xs">
                <span className="min-w-0 truncate">{entry.artistName} · {entry.title}</span>
                <span className="text-muted-foreground">{entry.releaseDate}</span>
              </div>
            ))}
          </CardContent>
        </Card>
      ) : null}

      {tab === 'history' ? (
        <Card>
          <CardHeader>
            <CardTitle>History</CardTitle>
            <CardDescription>Downloads, imports, failures, and renames.</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-1">
            {!loadedTabs.history ? (
              <p className="text-xs text-muted-foreground" role="status">Loading history…</p>
            ) : history.length === 0 ? (
              <p className="text-xs text-muted-foreground">No events yet.</p>
            ) : null}
            {history.map((entry) => (
              <p key={entry.id} className="text-xs text-muted-foreground">
                {new Date(entry.createdAt).toLocaleString()} · {entry.type} · {entry.message}
              </p>
            ))}
          </CardContent>
        </Card>
      ) : null}

      {tab === 'settings' && canReadSettings ? (
        config ? (
          <SettingsPanel config={config} canWrite={canWriteSettings} onSaved={refresh} />
        ) : (
          <p className="text-sm text-muted-foreground" role="status">Loading music settings…</p>
        )
      ) : null}

      {openAlbum ? (
        <AlbumDialog
          albumId={openAlbum}
          canWrite={canWrite}
          canReadSettings={canReadSettings}
          onClose={() => setOpenAlbum(null)}
          onChanged={refresh}
        />
      ) : null}
    </div>
  )
}
