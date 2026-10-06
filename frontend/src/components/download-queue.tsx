import { useState } from 'react'
import {
  ArrowRightIcon,
  FileDownIcon,
  InboxIcon,
  LoaderCircleIcon,
  PauseIcon,
  PlayIcon,
  RefreshCwIcon,
  RotateCwIcon,
  XIcon,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { errorMessage, type Job, type JobStatus, type OutputFile } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { formatAge, formatBytes } from '@/lib/format'

const statusLabels: Record<JobStatus, string> = {
  queued: 'Queued',
  downloading: 'Downloading',
  verifying: 'Verifying',
  repairing: 'Repairing',
  extracting: 'Extracting',
  paused: 'Paused',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
}

const statusClasses: Partial<Record<JobStatus, string>> = {
  completed: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-400',
  paused: 'border-amber-500/25 bg-amber-500/10 text-amber-400',
}

type RowAction = 'pause' | 'resume' | 'cancel' | 'retry'

function jobPercent(job: Job) {
  if (job.status === 'completed') return 100
  if (job.bytesTotal > 0) return Math.min(100, Math.round((job.bytesDone / job.bytesTotal) * 100))
  if (job.segmentsTotal > 0) return Math.min(100, Math.round((job.segmentsDone / job.segmentsTotal) * 100))
  return null
}

function jobSize(job: Job) {
  if (job.bytesTotal > 0) return `${formatBytes(job.bytesDone)} of ${formatBytes(job.bytesTotal)}`
  if (job.bytesDone > 0) return `${formatBytes(job.bytesDone)} downloaded`
  return 'Waiting for the transfer to start'
}

function jobTechnical(job: Job) {
  const parts = [`${job.segmentsDone}/${job.segmentsTotal} segments`]
  if (job.missingSegments > 0) parts.push(`${job.missingSegments} missing`)
  parts.push(`started ${formatAge(job.createdAt)}`, `updated ${formatAge(job.updatedAt)}`)
  return parts.join(' · ')
}

function JobProgress({ job }: { job: Job }) {
  const percent = jobPercent(job)
  if (percent === null) return null

  return (
    <div className="flex items-center gap-3">
      <div
        role="progressbar"
        aria-label={`${job.title} progress`}
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
      <span className="w-10 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
        {percent}%
      </span>
    </div>
  )
}

function OutputFiles({ files }: { files: OutputFile[] }) {
  return (
    <ul className="flex flex-col gap-1">
      {files.map((file) => (
        <li key={`${file.url}-${file.name}`}>
          <a
            href={file.url}
            download={file.name}
            className="inline-flex items-center gap-1.5 rounded-sm text-sm text-primary underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >
            <FileDownIcon aria-hidden="true" className="size-4 shrink-0" />
            <span className="break-all">{file.name}</span>
            <span className="text-muted-foreground">({formatBytes(file.size)})</span>
          </a>
        </li>
      ))}
    </ul>
  )
}

function ActionButton({
  action,
  pending,
  idle,
  icon: Icon,
  disabled,
  variant = 'outline',
  onClick,
}: {
  action: RowAction
  pending: RowAction | undefined
  idle: string
  icon: LucideIcon
  disabled: boolean
  variant?: 'outline' | 'destructive' | 'ghost' | 'default'
  onClick: () => void
}) {
  const busy = pending === action
  return (
    <Button size="sm" variant={variant} disabled={disabled || busy} onClick={onClick}>
      {busy ? (
        <LoaderCircleIcon
          data-icon="inline-start"
          aria-hidden="true"
          className="animate-spin motion-reduce:animate-none"
        />
      ) : (
        <Icon data-icon="inline-start" aria-hidden="true" />
      )}
      {busy ? `${idle}…` : idle}
    </Button>
  )
}

export function DownloadQueue({
  jobs,
  error,
  onRetry,
  onPause,
  onResume,
  onCancel,
  onRefresh,
  title = 'Download queue',
  description = 'Transfer and processing stages refresh automatically while jobs are active.',
  limit,
  emptyAction,
}: {
  jobs: Job[] | null
  error: string | null
  onRetry: (job: Job) => Promise<void>
  onPause?: (job: Job) => Promise<void>
  onResume?: (job: Job) => Promise<void>
  onCancel?: (job: Job) => Promise<void>
  onRefresh: () => void
  title?: string
  description?: string
  limit?: number
  emptyAction?: { label: string; href: string }
}) {
  const [pending, setPending] = useState<{ id: string; action: RowAction } | null>(null)
  const [rowError, setRowError] = useState<{ id: string; message: string } | null>(null)
  const [confirmCancelId, setConfirmCancelId] = useState<string | null>(null)
  const { can } = useAuth()
  const canWrite = can(accessPermissions.downloadsWrite)
  const canLibrary = can(accessPermissions.libraryRead)

  const run = async (job: Job, action: RowAction, handler: (job: Job) => Promise<void>) => {
    setPending({ id: job.id, action })
    setRowError(null)
    try {
      await handler(job)
      if (action === 'cancel') setConfirmCancelId(null)
    } catch (cause) {
      setRowError({ id: job.id, message: errorMessage(cause) })
    } finally {
      setPending(null)
    }
  }

  const visibleJobs = jobs && limit !== undefined ? jobs.slice(0, limit) : jobs
  const showViewAll = limit !== undefined && jobs !== null && jobs.length > limit

  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          {title}
        </CardTitle>
        <CardDescription>{description}</CardDescription>
        {showViewAll && (
          <CardAction>
            <Button asChild variant="ghost" size="sm">
              <a href="#usenet" aria-label="View all Usenet downloads">
                View all
                <ArrowRightIcon data-icon="inline-end" />
              </a>
            </Button>
          </CardAction>
        )}
      </CardHeader>
      <CardContent aria-busy={jobs === null}>
        {error && (
          <div
            role="alert"
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            <span>
              {jobs === null
                ? 'Could not load the download queue.'
                : 'Could not refresh the download queue.'}{' '}
              {error}
            </span>
            <Button size="sm" variant="outline" onClick={onRefresh}>
              <RefreshCwIcon data-icon="inline-start" />
              Retry
            </Button>
          </div>
        )}

        {jobs === null && !error && (
          <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
            Loading queue…
          </p>
        )}

        {jobs?.length === 0 && (
          <div className="flex flex-col items-start gap-3 rounded-md border border-dashed border-border px-4 py-6">
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <InboxIcon className="size-4" />
              No downloads yet. Search for a release and choose Download.
            </p>
            {emptyAction && canLibrary && (
              <Button asChild size="sm" variant="outline">
                <a href={emptyAction.href}>{emptyAction.label}</a>
              </Button>
            )}
          </div>
        )}

        {visibleJobs && visibleJobs.length > 0 && (
          <ul className="flex flex-col divide-y divide-border">
            {visibleJobs.map((job) => {
              const pendingAction = pending?.id === job.id ? pending.action : undefined
              const busy = pendingAction !== undefined
              const active = job.status !== 'completed' && job.status !== 'failed' && job.status !== 'cancelled'
              const canPause = canWrite && onPause !== undefined && active && job.status !== 'paused'
              const canResume = canWrite && onResume !== undefined && job.status === 'paused'
              const canCancel = canWrite && onCancel !== undefined && active
              const canRetry = canWrite && (job.status === 'failed' || job.status === 'cancelled')
              const confirmingCancel = confirmCancelId === job.id

              return (
                <li key={job.id} className="flex flex-col gap-2.5 py-3.5 first:pt-0 last:pb-0">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <p className="text-sm font-medium break-words">{job.title}</p>
                      <p className="text-xs text-muted-foreground">{jobSize(job)}</p>
                    </div>
                    <Badge
                      variant={job.status === 'failed' ? 'destructive' : 'outline'}
                      className={statusClasses[job.status]}
                    >
                      {statusLabels[job.status]}
                    </Badge>
                  </div>

                  <JobProgress job={job} />

                  {job.error && (
                    <p role="alert" className="text-sm text-destructive">
                      {job.error}
                    </p>
                  )}

                  {rowError?.id === job.id && (
                    <p role="alert" className="text-sm text-destructive">
                      {rowError.message}
                    </p>
                  )}

                  {confirmingCancel && (
                    <div
                      role="group"
                      aria-label={`Confirm cancelling ${job.title}`}
                      className="flex flex-col gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                    >
                      <p>
                        Cancel this download? The data already downloaded stays in the cache, so a retry
                        can still resume from it.
                      </p>
                      <div className="flex flex-wrap gap-2">
                        <Button
                          size="sm"
                          variant="destructive"
                          disabled={busy}
                          onClick={() => onCancel && void run(job, 'cancel', onCancel)}
                        >
                          Cancel download
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          autoFocus
                          disabled={busy}
                          onClick={() => setConfirmCancelId(null)}
                        >
                          <XIcon data-icon="inline-start" aria-hidden="true" />
                          Keep downloading
                        </Button>
                      </div>
                    </div>
                  )}

                  {(canPause || canResume || canCancel || canRetry) && (
                    <div className="flex flex-wrap items-center gap-2">
                      {canResume && onResume && (
                        <ActionButton
                          action="resume"
                          pending={pendingAction}
                          idle="Resume"
                          icon={PlayIcon}
                          disabled={busy}
                          onClick={() => void run(job, 'resume', onResume)}
                        />
                      )}
                      {canPause && onPause && (
                        <ActionButton
                          action="pause"
                          pending={pendingAction}
                          idle="Pause"
                          icon={PauseIcon}
                          disabled={busy}
                          onClick={() => void run(job, 'pause', onPause)}
                        />
                      )}
                      {canRetry && (
                        <ActionButton
                          action="retry"
                          pending={pendingAction}
                          idle="Retry"
                          icon={RotateCwIcon}
                          disabled={busy}
                          onClick={() => void run(job, 'retry', onRetry)}
                        />
                      )}
                      {canCancel && !confirmingCancel && (
                        <ActionButton
                          action="cancel"
                          pending={pendingAction}
                          idle="Cancel"
                          icon={XIcon}
                          variant="ghost"
                          disabled={busy}
                          onClick={() => setConfirmCancelId(job.id)}
                        />
                      )}
                    </div>
                  )}

                  <details className="rounded-md border border-border bg-background/50">
                    <summary className="cursor-pointer rounded-md px-3 py-1.5 text-xs font-medium text-muted-foreground select-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none">
                      {job.files.length > 0
                        ? `Transfer details and files (${job.files.length})`
                        : 'Transfer details'}
                    </summary>
                    <div className="flex flex-col gap-2 border-t border-border px-3 py-2">
                      <p className="text-xs text-muted-foreground">{jobTechnical(job)}</p>
                      {job.files.length > 0 && <OutputFiles files={job.files} />}
                    </div>
                  </details>
                </li>
              )
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
