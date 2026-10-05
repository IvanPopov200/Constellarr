import { useCallback, useEffect, useState } from 'react'
import { Activity, AlertTriangle, BellRing, Database, Gauge, HardDrive, LoaderCircle, RefreshCw } from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { AlertsPanel } from '@/components/operations-alerts-panel'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState, ErrorNote, LoadingNote } from '@/components/tv-ui'
import { errorMessage } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'
import { cn } from 'cn'
import { operationsApi, type OperationEvent, type OperationsStatus } from '@/lib/operations-api'

const severityTones = {
  info: 'border-sky-500/40 text-sky-300',
  warning: 'border-amber-500/40 text-amber-300',
  critical: 'border-destructive/40 bg-destructive/10 text-destructive',
} as const

export function OperationsPage() {
  const [status, setStatus] = useState<OperationsStatus | null>(null)
  const [events, setEvents] = useState<OperationEvent[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  const load = useCallback(async (signal?: AbortSignal) => {
    const [current, recent] = await Promise.all([operationsApi.status(signal), operationsApi.events(signal)])
    if (signal?.aborted) return
    setStatus(current)
    setEvents(recent)
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    Promise.all([operationsApi.status(controller.signal), operationsApi.events(controller.signal)])
      .then(([current, recent]) => {
        if (controller.signal.aborted) return
        setStatus(current)
        setEvents(recent)
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    const timer = window.setInterval(() => {
      load()
        .then(() => setError(''))
        .catch(() => undefined)
    }, 15000)
    return () => {
      window.clearInterval(timer)
      controller.abort()
    }
  }, [load])

  async function refresh() {
    setRefreshing(true)
    setError('')
    try {
      await load()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeading
        title="System"
        description="Server health, queues, metrics, structured events, and alerts."
        action={
          <Button size="sm" variant="outline" disabled={refreshing} onClick={() => void refresh()}>
            {refreshing ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <RefreshCw />}
            Refresh
          </Button>
        }
      />

      {error && <ErrorNote onRetry={() => void refresh()}>{error}</ErrorNote>}
      {loading && !status && <LoadingNote>Loading server status…</LoadingNote>}

      {status && (
        <>
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatusCard
              title="Database"
              icon={<Database className="size-4 text-muted-foreground" />}
              value={status.database === 'connected' ? 'Connected' : 'Unavailable'}
              tone={status.database === 'connected' ? 'ok' : 'bad'}
              detail="Pool health checked every 15 seconds"
            />
            <StatusCard
              title="Data volume"
              icon={<HardDrive className="size-4 text-muted-foreground" />}
              value={status.storage.known ? `${formatBytes(status.storage.freeBytes)} free` : 'Unknown'}
              tone={status.storage.known && status.storage.usedPercent > 90 ? 'warn' : 'ok'}
              detail={
                status.storage.known
                  ? `${status.storage.usedPercent}% of ${formatBytes(status.storage.totalBytes)} used`
                  : 'Storage could not be inspected'
              }
            />
            <StatusCard
              title="Queues"
              icon={<Activity className="size-4 text-muted-foreground" />}
              value={`${status.downloads.active + status.torrents.active} active`}
              tone={status.downloads.failed + status.torrents.failed > 0 ? 'warn' : 'ok'}
              detail={`Usenet ${status.downloads.queued} queued · torrents ${status.torrents.queued} queued · ${status.torrents.seeding} seeding`}
            />
            <StatusCard
              title="Alerts"
              icon={<BellRing className="size-4 text-muted-foreground" />}
              value={status.alerts.firing > 0 ? `${status.alerts.firing} firing` : 'All clear'}
              tone={status.alerts.firing > 0 ? 'warn' : 'ok'}
              detail={`${status.alerts.total} rules configured`}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <Card className="shadow-none">
              <CardHeader className="border-b border-border">
                <CardTitle className="flex items-center gap-2">
                  <Gauge className="size-4 text-muted-foreground" />
                  Library
                </CardTitle>
              </CardHeader>
              <CardContent className="text-sm">
                <p>{status.library.movies} movies · {status.library.series} series · {status.library.episodes} episodes</p>
                <p className="mt-0.5 text-sm">
                  {status.library.artists} artists · {status.library.albums} albums · {status.library.tracks} tracks
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  Prometheus metrics are exposed at <code>{status.metricsPath}</code> with Go, process, database pool, and
                  transfer counters. Labels never contain titles, paths, or user names.
                </p>
              </CardContent>
            </Card>
            <Card className="shadow-none">
              <CardHeader className="border-b border-border">
                <CardTitle className="flex items-center gap-2">
                  <AlertTriangle className="size-4 text-muted-foreground" />
                  Recorded failures
                </CardTitle>
              </CardHeader>
              <CardContent className="text-sm">
                <p>{status.failures.downloadFailures} downloads · {status.failures.importErrors} imports · {status.failures.providerFailures} providers</p>
                <p className="mt-1 text-xs text-muted-foreground">
                  Failures recorded since this database was created; identical failures are stored once.
                </p>
              </CardContent>
            </Card>
          </div>
        </>
      )}

      <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle className="flex items-center gap-2">
            <Activity className="size-4 text-muted-foreground" />
            Recent events
          </CardTitle>
          <CardDescription>Download, import, provider, alert, and backup activity recorded by the server.</CardDescription>
        </CardHeader>
        <CardContent className="gap-2">
          {events.length === 0 && <EmptyState>No events have been recorded yet.</EmptyState>}
          <ul className="space-y-1 text-sm">
            {events.slice(0, 20).map((event) => (
              <li key={event.id} className="flex flex-wrap items-center gap-2 border-b border-border/50 pb-1 last:border-0">
                <Badge variant="outline" className={cn(severityTones[event.severity])}>
                  {event.kind.replace(/_/g, ' ')}
                </Badge>
                <span className="min-w-0 flex-1 truncate" title={event.message ?? ''}>
                  {event.message || event.source || '—'}
                </span>
                <span className="text-xs text-muted-foreground">{formatAge(event.at)}</span>
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      <AlertsPanel />
    </div>
  )
}

function StatusCard({
  title,
  icon,
  value,
  detail,
  tone,
}: {
  title: string
  icon: React.ReactNode
  value: string
  detail: string
  tone: 'ok' | 'warn' | 'bad'
}) {
  return (
    <Card className="shadow-none">
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-2 text-sm font-medium">
          {icon}
          {title}
        </CardTitle>
      </CardHeader>
      <CardContent className="gap-1">
        <p
          className={cn(
            'text-lg font-semibold',
            tone === 'bad' && 'text-destructive',
            tone === 'warn' && 'text-amber-300',
          )}
        >
          {value}
        </p>
        <p className="text-xs text-muted-foreground">{detail}</p>
      </CardContent>
    </Card>
  )
}
