import { useCallback, useEffect, useRef, useState } from 'react'
import { XIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { errorMessage } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { torrentsApi, type TorrentDetail } from '@/lib/torrents-api'
import { displayStatus, formatRatio, progressPercent, statusLabels, statusVariant } from '@/components/torrents-shared'

type Props = { id: string; canWrite: boolean; onClose: () => void; onChanged: () => void }

export function TorrentDetailDialog({ id, canWrite, onClose, onChanged }: Props) {
  const [detail, setDetail] = useState<TorrentDetail | null>(null)
  const [error, setError] = useState<string | null>(null)
  const closeRef = useRef<HTMLButtonElement>(null)

  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        setDetail(await torrentsApi.detail(id, signal))
        setError(null)
      } catch (cause) {
        if (signal?.aborted) return
        setError(errorMessage(cause))
      }
    },
    [id],
  )

  useEffect(() => {
    const controller = new AbortController()
    const tick = () => void load(controller.signal)
    const first = window.setTimeout(tick, 0)
    const timer = window.setInterval(tick, 1500)
    return () => {
      controller.abort()
      window.clearTimeout(first)
      window.clearInterval(timer)
    }
  }, [load])

  useEffect(() => {
    closeRef.current?.focus()
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const job = detail?.job

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4"
      role="dialog"
      aria-modal="true"
      aria-label="Torrent details"
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div className="flex max-h-full w-full max-w-2xl flex-col gap-4 overflow-y-auto rounded-xl bg-card p-6 shadow-lg ring-1 ring-foreground/10">
        <div className="flex items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <h2 className="text-base font-medium">{job?.name || 'Torrent'}</h2>
            <span className="text-xs break-all text-muted-foreground">{job?.infoHash}</span>
          </div>
          <Button ref={closeRef} size="icon-sm" variant="ghost" aria-label="Close details" onClick={onClose}>
            <XIcon aria-hidden="true" />
          </Button>
        </div>

        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : null}

        {job ? (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={statusVariant(displayStatus(job))}>{statusLabels[displayStatus(job)]}</Badge>
              <Badge variant="outline">{job.source}</Badge>
              {job.private ? <Badge variant="outline">Private</Badge> : null}
              <span className="text-xs text-muted-foreground">
                {formatBytes(job.bytesDone)} / {formatBytes(job.bytesTotal)} · {progressPercent(job)}% · ratio{' '}
                {formatRatio(job.ratio)}
              </span>
            </div>
            {job.error ? (
              <p role="alert" className="text-sm text-destructive">
                {job.error}
              </p>
            ) : null}
            {job.processing?.state === 'failed' ? (
              <p role="alert" className="text-sm text-destructive">
                {job.processing.error || 'The download could not be prepared for import.'}
              </p>
            ) : null}
            {job.processing?.state === 'pending' || job.processing?.state === 'running' ? (
              <p role="status" className="text-sm text-muted-foreground">
                Preparing the download for import; the original files keep seeding.
              </p>
            ) : null}

            <section className="flex flex-col gap-2">
              <h3 className="text-sm font-medium">Files</h3>
              {job.files.length === 0 ? (
                <p className="text-xs text-muted-foreground">No file list is available yet.</p>
              ) : (
                <ul className="flex flex-col gap-1">
                  {job.files.map((file) => (
                    <li
                      key={file.name}
                      className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-1.5 text-xs"
                    >
                      <span className="truncate">{file.name}</span>
                      <span className="shrink-0 text-muted-foreground">
                        {file.url ? (
                          <a
                            href={file.url}
                            download
                            className="text-primary underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                          >
                            {formatBytes(file.done)} / {formatBytes(file.size)}
                          </a>
                        ) : (
                          `${formatBytes(file.done)} / ${formatBytes(file.size)}`
                        )}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section className="flex flex-col gap-2">
              <h3 className="text-sm font-medium">Peers</h3>
              {detail && detail.peers.length > 0 ? (
                <table className="w-full text-left text-xs">
                  <thead className="text-muted-foreground">
                    <tr>
                      <th scope="col" className="py-1 font-medium">
                        Address
                      </th>
                      <th scope="col" className="py-1 font-medium">
                        Client
                      </th>
                      <th scope="col" className="py-1 font-medium">
                        Direction
                      </th>
                      <th scope="col" className="py-1 text-right font-medium">
                        Progress
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.peers.map((peer) => (
                      <tr key={`${peer.address}-${peer.direction}`} className="border-t border-border">
                        <td className="py-1 font-mono">{peer.address}</td>
                        <td className="py-1">{peer.client || '—'}</td>
                        <td className="py-1">{peer.direction}</td>
                        <td className="py-1 text-right tabular-nums">{Math.round(peer.progress * 100)}%</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <p className="text-xs text-muted-foreground">No peers are connected.</p>
              )}
            </section>

            {canWrite ? (
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  void torrentsApi.recheck(job.id).then(onChanged).catch((cause) => setError(errorMessage(cause)))
                }}
              >
                Recheck
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  void torrentsApi
                    .setLimits(job.id, 0, 0)
                    .then(() => {
                      onChanged()
                      void load()
                    })
                    .catch((cause) => setError(errorMessage(cause)))
                }}
              >
                Stop seeding
              </Button>
            </div>
            ) : null}
          </>
        ) : !error ? (
          <p className="text-sm text-muted-foreground">Loading details…</p>
        ) : null}
      </div>
    </div>
  )
}
