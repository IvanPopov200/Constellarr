import { useMemo } from 'react'
import {
  ActivityIcon,
  ArrowDownToLineIcon,
  CircleAlertIcon,
  CircleCheckIcon,
  SearchIcon,
  ShieldAlertIcon,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { DownloadQueue } from '@/components/download-queue'
import { SourcesCard } from '@/components/sources-card'
import { MovieOverview } from '@/components/movie-overview'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { isActiveJob, type Job } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { formatBytes } from '@/lib/format'
import { cn } from 'cn'

function Stat({
  label,
  value,
  detail,
  icon: Icon,
  tone = 'default',
}: {
  label: string
  value: string
  detail: string
  icon: LucideIcon
  tone?: 'default' | 'danger'
}) {
  return (
    <Card size="sm">
      <CardContent className="gap-2">
        <div className="flex items-center justify-between gap-2">
          <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            {label}
          </p>
          <Icon className="size-4 text-muted-foreground/70" aria-hidden="true" />
        </div>
        <p
          className={cn(
            'font-heading text-2xl font-semibold tabular-nums',
            tone === 'danger' && 'text-destructive',
          )}
        >
          {value}
        </p>
        <p className="text-xs text-muted-foreground">{detail}</p>
      </CardContent>
    </Card>
  )
}

export function Overview({
  jobs,
  error,
  onRetry,
  onPause,
  onResume,
  onCancel,
  onRefresh,
}: {
  jobs: Job[] | null
  error: string | null
  onRetry: (job: Job) => Promise<void>
  onPause: (job: Job) => Promise<void>
  onResume: (job: Job) => Promise<void>
  onCancel: (job: Job) => Promise<void>
  onRefresh: () => void
}) {
  const { can } = useAuth()
  const canLibrary = can(accessPermissions.libraryRead)
  const canDownloads = can(accessPermissions.downloadsRead)
  const canSettings = can(accessPermissions.settingsRead)

  const metrics = useMemo(() => {
    const list = jobs ?? []
    const completed = list.filter((job) => job.status === 'completed')
    return {
      active: list.filter(isActiveJob).length,
      completed: completed.length,
      failed: list.filter((job) => job.status === 'failed').length,
      downloaded: completed.reduce((total, job) => total + Math.max(0, job.bytesDone), 0),
    }
  }, [jobs])

  const loading = jobs === null
  const value = (count: number) => (loading ? '—' : String(count))

  // Nothing on this page is readable for a role without library, downloads, or settings grants.
  if (!canLibrary && !canDownloads && !canSettings) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeading title="Overview" description="Download activity and source connectivity for this server." />
        <Card className="max-w-2xl">
          <CardContent className="items-start gap-3 py-2">
            <p className="flex items-center gap-2 font-medium">
              <ShieldAlertIcon className="size-4 text-primary" aria-hidden="true" />
              Welcome to Constellarr
            </p>
            <p className="text-sm text-muted-foreground">
              Your account does not include library, download, or server settings access yet. Ask an administrator to
              assign a role with the permissions you need.
            </p>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeading
        title="Overview"
        description="Recent download activity and source connectivity for this server."
        action={
          canLibrary ? (
            <Button asChild size="sm">
              <a href="#movies">
                <SearchIcon data-icon="inline-start" />
                Search releases
              </a>
            </Button>
          ) : undefined
        }
      />

      {canDownloads && (
        <>
          {loading && (
            <p role="status" className="sr-only">
              Loading download metrics…
            </p>
          )}
          <section
            aria-label="Download metrics"
            aria-busy={loading}
            className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4"
          >
            <Stat
              label="Active"
              value={value(metrics.active)}
              detail="Queued, transferring, or processing"
              icon={ActivityIcon}
            />
            <Stat
              label="Completed"
              value={value(metrics.completed)}
              detail={loading || metrics.completed > 0 ? 'Finished successfully' : 'No finished jobs yet'}
              icon={CircleCheckIcon}
            />
            <Stat
              label="Needs attention"
              value={value(metrics.failed)}
              detail={
                loading || metrics.failed > 0 ? 'Failed downloads awaiting retry' : 'No failed downloads'
              }
              icon={CircleAlertIcon}
              tone={metrics.failed > 0 ? 'danger' : 'default'}
            />
            <Stat
              label="Downloaded"
              value={loading ? '—' : formatBytes(metrics.downloaded)}
              detail="Completed job payload"
              icon={ArrowDownToLineIcon}
            />
          </section>
        </>
      )}

      {canLibrary && <MovieOverview />}

      {(canDownloads || canSettings) && (
        <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
          {canDownloads && (
            <DownloadQueue
              jobs={jobs}
              error={error}
              onRetry={onRetry}
              onPause={onPause}
              onResume={onResume}
              onCancel={onCancel}
              onRefresh={onRefresh}
              title="Recent downloads"
              description="Pause, resume, or cancel a download."
              limit={5}
              emptyAction={{ label: 'Search releases', href: '#movies' }}
            />
          )}
          {canSettings && <SourcesCard />}
        </div>
      )}
    </div>
  )
}
