import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { ActivityIcon, PlusIcon, UploadIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { isActiveTorrent, torrentsApi, type TorrentHealth, type TorrentJob, type TorrentSource } from '@/lib/torrents-api'
import { fileToBase64, magnetIsValid } from '@/components/torrents-shared'
import { TorrentQueueRow } from '@/components/torrents-queue-row'
import { TorrentDetailDialog } from '@/components/torrents-detail'
import { TorrentSearch } from '@/components/torrents-search'
import { TorrentSettingsCard } from '@/components/torrents-settings'

const activeIntervalMs = 1500
const idleIntervalMs = 5000

export function TorrentsPage() {
  const { can } = useAuth()
  const canReadQueue = can(accessPermissions.downloadsRead)
  const canWriteQueue = can(accessPermissions.downloadsWrite)
  const canReadSettings = can(accessPermissions.settingsRead)
  const canWriteSettings = can(accessPermissions.settingsWrite)

  const [jobs, setJobs] = useState<TorrentJob[] | null>(null)
  const [health, setHealth] = useState<TorrentHealth | null>(null)
  const [sources, setSources] = useState<TorrentSource[]>([])
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [magnet, setMagnet] = useState('')
  const [detailId, setDetailId] = useState<string | null>(null)
  const [version, setVersion] = useState(0)
  const jobsRef = useRef<TorrentJob[] | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!canReadQueue) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let stopped = false

    const poll = async () => {
      try {
        const next = await torrentsApi.list(controller.signal)
        if (stopped) return
        jobsRef.current = next
        setJobs(next)
        setError(null)
      } catch (cause) {
        if (stopped || controller.signal.aborted) return
        setError(errorMessage(cause))
      }
      if (stopped) return
      const active = (jobsRef.current ?? []).some(isActiveTorrent)
      timer = setTimeout(poll, active ? activeIntervalMs : idleIntervalMs)
    }

    void poll()
    return () => {
      stopped = true
      controller.abort()
      clearTimeout(timer)
    }
  }, [version, canReadQueue])

  useEffect(() => {
    if (!canReadQueue) return
    const controller = new AbortController()
    let stopped = false
    const load = async () => {
      try {
        const status = await torrentsApi.health(controller.signal)
        if (!stopped) setHealth(status)
      } catch {
        if (!stopped) setHealth(null)
      }
    }
    const first = window.setTimeout(() => void load(), 0)
    const timer = window.setInterval(() => void load(), 10_000)
    return () => {
      stopped = true
      controller.abort()
      window.clearTimeout(first)
      window.clearInterval(timer)
    }
  }, [canReadQueue])

  const reloadSources = useCallback(() => {
    if (!canReadSettings) return
    torrentsApi
      .listSources()
      .then(setSources)
      .catch((cause) => setError(errorMessage(cause)))
  }, [canReadSettings])

  useEffect(() => {
    reloadSources()
  }, [reloadSources])

  const refresh = useCallback(() => setVersion((current) => current + 1), [])

  const runAction = useCallback(
    async (action: (id: string) => Promise<unknown>, id: string) => {
      setBusy(true)
      setError(null)
      try {
        await action(id)
        refresh()
      } catch (cause) {
        setError(errorMessage(cause))
      } finally {
        setBusy(false)
      }
    },
    [refresh],
  )

  const addMagnet = async (event: FormEvent) => {
    event.preventDefault()
    const value = magnet.trim()
    if (!magnetIsValid(value)) {
      setError('Enter a valid magnet link.')
      return
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const job = await torrentsApi.add({ magnet: value })
      setNotice(`Added ${job.name || job.title || job.infoHash}.`)
      setMagnet('')
      refresh()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  const addTorrentFile = async (file: File) => {
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const encoded = await fileToBase64(file)
      const job = await torrentsApi.add({ torrent: encoded, filename: file.name })
      setNotice(`Added ${job.name || job.title || job.infoHash}.`)
      refresh()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
      if (fileInput.current) fileInput.current.value = ''
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-medium">Torrents</h1>
          <p className="text-sm text-muted-foreground">
            Built-in BitTorrent engine for magnets, torrent files and Torznab results.
          </p>
        </div>
        {canReadQueue ? (
          <Button variant="outline" size="sm" onClick={refresh}>
            Refresh
          </Button>
        ) : null}
      </div>

      {canReadQueue && health ? (
        <p role="status" className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <ActivityIcon aria-hidden="true" className="size-3.5" />
          {health.ok ? 'Engine healthy' : `Engine problem: ${health.error || 'unknown'}`} · port{' '}
          {health.listenPort || '—'} · {health.activeJobs} active · {health.queuedJobs} queued ·{' '}
          {health.processingJobs} preparing · {health.failedJobs} failed
        </p>
      ) : null}

      {error && (canReadQueue || canWriteQueue) ? (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      ) : null}

      {canWriteQueue ? (
        <Card>
          <CardHeader>
            <CardTitle>Add a torrent</CardTitle>
            <CardDescription>Paste a magnet link or upload a .torrent file.</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <form className="flex flex-wrap items-end gap-2" onSubmit={addMagnet}>
              <label className="flex min-w-64 flex-1 flex-col gap-1 text-sm">
                Magnet link
                <Input
                  value={magnet}
                  onChange={(event) => setMagnet(event.target.value)}
                  placeholder="Paste a magnet link"
                  spellCheck={false}
                />
              </label>
              <Button type="submit" disabled={busy}>
                <PlusIcon aria-hidden="true" />
                Add magnet
              </Button>
            </form>
            <div className="flex flex-wrap items-center gap-2">
              <input
                ref={fileInput}
                type="file"
                accept=".torrent,application/x-bittorrent"
                className="hidden"
                aria-label="Torrent file"
                onChange={(event) => {
                  const file = event.target.files?.[0]
                  if (file) void addTorrentFile(file)
                }}
              />
              <Button type="button" variant="outline" disabled={busy} onClick={() => fileInput.current?.click()}>
                <UploadIcon aria-hidden="true" />
                Upload .torrent
              </Button>
              <span className="text-xs text-muted-foreground">
                Uploaded torrent files stay on the server with the download.
              </span>
            </div>
            {notice ? (
              <p role="status" className="text-sm text-muted-foreground">
                {notice}
              </p>
            ) : null}
          </CardContent>
        </Card>
      ) : null}

      {canReadQueue ? (
        <Card>
          <CardHeader>
            <CardTitle>Queue</CardTitle>
            <CardDescription>
              Pausing keeps the data. Seeding stops when the ratio or time limit is reached; a ratio of 0 stops as soon
              as the download completes. Archived downloads are extracted for import while the original files keep
              seeding.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {jobs === null ? (
              <p className="text-sm text-muted-foreground">Loading torrents…</p>
            ) : jobs.length === 0 ? (
              <p className="text-sm text-muted-foreground">Nothing in the queue yet.</p>
            ) : (
              <ul className="flex flex-col gap-3">
                {jobs.map((job) => (
                  <TorrentQueueRow
                    key={`${job.id}:${job.seedRatioLimit}:${job.seedTimeLimitMinutes}`}
                    job={job}
                    busy={busy}
                    canWrite={canWriteQueue}
                    onAction={(action) => void runAction(action, job.id)}
                    onOpen={setDetailId}
                    onChanged={refresh}
                  />
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      ) : null}

      {canReadQueue ? (
        <TorrentSearch sources={sources} canAdd={canWriteQueue} canReadSources={canReadSettings} onAdded={refresh} />
      ) : null}
      {canReadSettings ? (
        <TorrentSettingsCard sources={sources} canWrite={canWriteSettings} onSourcesChanged={reloadSources} />
      ) : null}

      {!canReadQueue && !canReadSettings ? (
        <Card>
          <CardHeader>
            <CardTitle>No torrent access</CardTitle>
            <CardDescription>
              Your account can neither view the download queue nor manage torrent settings. Ask an administrator for the
              download or settings permission.
            </CardDescription>
          </CardHeader>
        </Card>
      ) : null}

      {!canReadQueue && canReadSettings ? (
        <Card>
          <CardHeader>
            <CardTitle>Queue access</CardTitle>
            <CardDescription>
              Your account can configure torrents, but viewing the download queue requires the download permission.
            </CardDescription>
          </CardHeader>
        </Card>
      ) : null}

      {detailId ? (
        <TorrentDetailDialog
          id={detailId}
          canWrite={canWriteQueue}
          onClose={() => setDetailId(null)}
          onChanged={refresh}
        />
      ) : null}
    </div>
  )
}
