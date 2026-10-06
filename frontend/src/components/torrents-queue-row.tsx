import { useState } from 'react'
import { HardDriveIcon, PauseIcon, PlayIcon, RotateCwIcon, Trash2Icon, XIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, errorMessage } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'
import { torrentsApi, type TorrentJob } from '@/lib/torrents-api'
import {
  displayStatus,
  formatETA,
  formatRate,
  formatRatio,
  formatSeedLimit,
  isExtracting,
  progressPercent,
  statusLabels,
  statusVariant,
} from '@/components/torrents-shared'

type Props = {
  job: TorrentJob
  busy: boolean
  canWrite: boolean
  onAction: (action: (id: string) => Promise<unknown>) => void
  onOpen: (id: string) => void
  onChanged: () => void
}

type Confirmation = 'cancel' | 'remove' | 'remove-files'

export function TorrentQueueRow({ job, busy, canWrite, onAction, onOpen, onChanged }: Props) {
  const [ratio, setRatio] = useState(String(job.seedRatioLimit))
  const [minutes, setMinutes] = useState(String(job.seedTimeLimitMinutes))
  const [limitError, setLimitError] = useState<string | null>(null)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)

  const percent = progressPercent(job)
  const status = displayStatus(job)
  const extractionFailed = job.processing?.state === 'failed'
  const finished = job.status === 'completed' || job.status === 'failed' || job.status === 'cancelled'
  const paused = job.status === 'paused'

  const saveLimits = async () => {
    const parsedRatio = Number(ratio)
    const parsedMinutes = Number(minutes)
    if (!Number.isFinite(parsedRatio) || parsedRatio < 0 || !Number.isInteger(parsedMinutes) || parsedMinutes < 0) {
      setLimitError('Enter a ratio of 0 or more and whole minutes.')
      return
    }
    setLimitError(null)
    try {
      await torrentsApi.setLimits(job.id, parsedRatio, parsedMinutes)
      onChanged()
    } catch (cause) {
      setLimitError(errorMessage(cause))
    }
  }

  const confirmAction = () => {
    const action = confirmation
    setConfirmation(null)
    if (action === 'cancel') onAction((id) => api.cancelDownload(id))
    else if (action === 'remove') onAction((id) => torrentsApi.remove(id, false))
    else if (action === 'remove-files') onAction((id) => torrentsApi.remove(id, true))
  }

  const confirmationText: Record<Confirmation, string> = {
    cancel: 'Cancel this download? The data downloaded so far stays in the cache; nothing is deleted.',
    remove: `Remove “${job.name}” from the queue? The downloaded files stay on disk.`,
    'remove-files': `Remove “${job.name}” and delete its downloaded files? This cannot be undone.`,
  }
  const confirmationLabel: Record<Confirmation, string> = {
    cancel: 'Cancel download',
    remove: 'Remove from queue',
    'remove-files': 'Remove and delete files',
  }

  return (
    <li className="flex flex-col gap-3 rounded-lg border border-border bg-background/40 px-4 py-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <button
            type="button"
            onClick={() => onOpen(job.id)}
            className="truncate text-left text-sm font-medium underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >
            {job.name || job.title || job.infoHash}
          </button>
          <span className="text-xs text-muted-foreground">
            {formatBytes(job.bytesDone)} of {formatBytes(job.bytesTotal)} · {formatRate(job.downloadRate)} down ·
            ETA {formatETA(job.etaSeconds)}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={statusVariant(status)}>{statusLabels[status]}</Badge>
          {isExtracting(job) ? <Badge variant="outline">Preparing import</Badge> : null}
          {job.private ? <Badge variant="outline">Private</Badge> : null}
          <Badge variant="outline">{job.source}</Badge>
        </div>
      </div>

      <div className="flex items-center gap-3">
        <div
          role="progressbar"
          aria-label={`${job.name} progress`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          className="h-1.5 w-full overflow-hidden rounded-full bg-muted"
        >
          <div
            className="h-full rounded-full bg-primary transition-[width] duration-500 motion-reduce:transition-none"
            style={{ width: `${percent}%` }}
          />
        </div>
        <span className="w-10 shrink-0 text-right text-xs tabular-nums text-muted-foreground">{percent}%</span>
      </div>

      {job.error ? (
        <p role="alert" className="text-xs text-destructive">
          {job.error}
        </p>
      ) : null}
      {extractionFailed ? (
        <p role="alert" className="text-xs text-destructive">
          {job.processing?.error || 'The download could not be prepared for import.'}
        </p>
      ) : null}

      {confirmation ? (
        <div
          role="group"
          aria-label={`Confirm: ${confirmationLabel[confirmation]}`}
          className="flex flex-col gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <p>{confirmationText[confirmation]}</p>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="destructive" disabled={busy} onClick={confirmAction}>
              {confirmationLabel[confirmation]}
            </Button>
            <Button size="sm" variant="ghost" autoFocus disabled={busy} onClick={() => setConfirmation(null)}>
              Keep it
            </Button>
          </div>
        </div>
      ) : null}

      {canWrite ? (
        <div className="flex flex-wrap items-center gap-2">
          {extractionFailed ? (
            <Button size="sm" disabled={busy} onClick={() => onAction((id) => torrentsApi.resume(id))}>
              <RotateCwIcon aria-hidden="true" />
              Retry import
            </Button>
          ) : null}
          {paused ? (
            <Button size="sm" disabled={busy} onClick={() => onAction((id) => torrentsApi.resume(id))}>
              <PlayIcon aria-hidden="true" />
              Resume
            </Button>
          ) : (
            <Button
              size="sm"
              variant="outline"
              disabled={busy || finished}
              onClick={() => onAction((id) => torrentsApi.pause(id))}
            >
              <PauseIcon aria-hidden="true" />
              Pause
            </Button>
          )}
          <Button size="sm" variant="outline" disabled={busy} onClick={() => onAction((id) => torrentsApi.recheck(id))}>
            <RotateCwIcon aria-hidden="true" />
            Recheck
          </Button>
          {!finished ? (
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setConfirmation('cancel')}>
              <XIcon aria-hidden="true" />
              Cancel download
            </Button>
          ) : null}
          <Button size="sm" variant="destructive" disabled={busy} onClick={() => setConfirmation('remove')}>
            <Trash2Icon aria-hidden="true" />
            Remove
          </Button>
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => setConfirmation('remove-files')}>
            <HardDriveIcon aria-hidden="true" />
            Remove files
          </Button>
        </div>
      ) : null}
      {limitError ? (
        <p role="alert" className="text-xs text-destructive">
          {limitError}
        </p>
      ) : null}

      <details className="rounded-md border border-border bg-background/50">
        <summary className="cursor-pointer rounded-md px-3 py-1.5 text-xs font-medium text-muted-foreground select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none">
          Details and seeding limits
        </summary>
        <div className="flex flex-col gap-3 border-t border-border px-3 py-3">
          <p className="text-xs text-muted-foreground">
            {job.piecesDone}/{job.piecesTotal} pieces · ratio {formatRatio(job.ratio)} · {formatRate(job.uploadRate)} up
            · {formatBytes(job.uploaded)} uploaded · {job.peers} peers · {job.seeds} seeds
          </p>
          <p className="text-xs text-muted-foreground">
            {formatSeedLimit(job.seedRatioLimit, job.seedTimeLimitMinutes)}
            {job.status === 'seeding' ? ` · seeding ${Math.round(job.seedingElapsedSeconds / 60)}m` : ''} · updated{' '}
            {formatAge(job.updatedAt)}
          </p>
          {canWrite ? (
            <div className="flex flex-wrap items-end gap-2">
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                Seed ratio
                <Input
                  className="h-8 w-20"
                  inputMode="decimal"
                  value={ratio}
                  aria-label={`${job.name} seed ratio limit`}
                  onChange={(event) => setRatio(event.target.value)}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs text-muted-foreground">
                Seed minutes
                <Input
                  className="h-8 w-24"
                  inputMode="numeric"
                  value={minutes}
                  aria-label={`${job.name} seed time limit in minutes`}
                  onChange={(event) => setMinutes(event.target.value)}
                />
              </label>
              <Button size="sm" variant="outline" disabled={busy} onClick={saveLimits}>
                Save limits
              </Button>
            </div>
          ) : null}
        </div>
      </details>
    </li>
  )
}
