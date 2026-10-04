import { useState } from 'react'
import { FileDownIcon, InboxIcon, LoaderCircleIcon, RefreshCwIcon, RotateCwIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { errorMessage, type Job, type JobStatus, type OutputFile } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'

const statusLabels: Record<JobStatus, string> = {
  queued: 'Queued',
  downloading: 'Downloading',
  verifying: 'Verifying',
  repairing: 'Repairing',
  extracting: 'Extracting',
  completed: 'Completed',
  failed: 'Failed',
}

function jobMeta(job: Job) {
  const parts = [
    `${formatBytes(job.bytesDone)} downloaded`,
    `${job.segmentsDone}/${job.segmentsTotal} segments`,
  ]
  if (job.missingSegments > 0) parts.push(`${job.missingSegments} missing`)
  parts.push(`updated ${formatAge(job.updatedAt)}`)
  return parts.join(' · ')
}

function JobProgress({ job }: { job: Job }) {
  if (job.segmentsTotal <= 0) return null
  const percent = job.status === 'completed' ? 100
    : Math.min(100, Math.round((job.segmentsDone / job.segmentsTotal) * 100))

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
          className="h-full rounded-full bg-primary transition-[width] duration-500"
          style={{ width: `${percent}%` }}
        />
      </div>
      <span className="w-10 shrink-0 text-right text-sm tabular-nums text-muted-foreground">
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
            className="inline-flex items-center gap-1.5 text-sm text-primary underline-offset-4 hover:underline"
          >
            <FileDownIcon className="size-4 shrink-0" />
            <span className="break-all">{file.name}</span>
            <span className="text-muted-foreground">({formatBytes(file.size)})</span>
          </a>
        </li>
      ))}
    </ul>
  )
}

export function DownloadQueue({
  jobs,
  error,
  onRetry,
  onRefresh,
}: {
  jobs: Job[] | null
  error: string | null
  onRetry: (job: Job) => Promise<void>
  onRefresh: () => void
}) {
  const [retryingId, setRetryingId] = useState<string | null>(null)
  const [retryError, setRetryError] = useState<string | null>(null)

  const retry = async (job: Job) => {
    setRetryingId(job.id)
    setRetryError(null)
    try {
      await onRetry(job)
    } catch (cause) {
      setRetryError(errorMessage(cause))
    } finally {
      setRetryingId(null)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          Download queue
        </CardTitle>
        <CardDescription>
          Transfer and processing stages refresh automatically while jobs are active.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {error && (
          <div
            role="alert"
            className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            <span>
              {jobs === null ? 'Could not load the download queue.' : 'Could not refresh the download queue.'}{' '}
              {error}
            </span>
            <Button size="sm" variant="outline" onClick={onRefresh}>
              <RefreshCwIcon data-icon="inline-start" />
              Retry
            </Button>
          </div>
        )}

        {retryError && (
          <p role="alert" className="text-sm text-destructive">
            {retryError}
          </p>
        )}

        {jobs === null && !error && (
          <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <LoaderCircleIcon className="size-4 animate-spin" />
            Loading queue…
          </p>
        )}

        {jobs?.length === 0 && (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <InboxIcon className="size-4" />
            No downloads yet. Search for a release and choose Download.
          </p>
        )}

        {jobs && jobs.length > 0 && (
          <ul className="flex flex-col divide-y">
            {jobs.map((job) => (
              <li key={job.id} className="flex flex-col gap-3 py-4 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="font-medium break-words">{job.title}</p>
                    <p className="text-sm text-muted-foreground">{jobMeta(job)}</p>
                  </div>
                  <Badge
                    variant={
                      job.status === 'failed'
                        ? 'destructive'
                        : job.status === 'completed'
                          ? 'default'
                          : 'secondary'
                    }
                  >
                    {statusLabels[job.status]}
                  </Badge>
                </div>

                <JobProgress job={job} />

                {job.status === 'failed' && (
                  <p role="alert" className="text-sm text-destructive">
                    {job.error ?? 'The download failed.'}
                  </p>
                )}

                {job.status === 'completed' && job.files.length > 0 && (
                  <OutputFiles files={job.files} />
                )}

                {job.status === 'failed' && (
                  <div>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={retryingId === job.id}
                      onClick={() => void retry(job)}
                    >
                      {retryingId === job.id ? (
                        <LoaderCircleIcon data-icon="inline-start" className="animate-spin" />
                      ) : (
                        <RotateCwIcon data-icon="inline-start" />
                      )}
                      {retryingId === job.id ? 'Retrying…' : 'Retry'}
                    </Button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
