import { useMemo } from 'react'
import {
  ActivityIcon,
  ArrowDownToLineIcon,
  CircleAlertIcon,
  CircleCheckIcon,
  SearchIcon,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { DownloadQueue } from '@/components/download-queue'
import { SourcesCard } from '@/components/sources-card'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { isActiveJob, type Job } from '@/lib/api'
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
  onRefresh,
}: {
  jobs: Job[] | null
  error: string | null
  onRetry: (job: Job) => Promise<void>
  onRefresh: () => void
}) {
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

  return (
    <div className="flex flex-col gap-6">
      <PageHeading
        title="Overview"
        description="Recent download activity and source connectivity for this server."
        action={
          <Button asChild size="sm">
            <a href="#search">
              <SearchIcon data-icon="inline-start" />
              Search releases
            </a>
          </Button>
        }
      />

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

      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <DownloadQueue
          jobs={jobs}
          error={error}
          onRetry={onRetry}
          onRefresh={onRefresh}
          title="Recent downloads"
          description="Latest queue activity, with retry and completed file links."
          limit={5}
          emptyAction={{ label: 'Search releases', href: '#search' }}
        />
        <SourcesCard />
      </div>
    </div>
  )
}
