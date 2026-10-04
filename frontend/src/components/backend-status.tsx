import { useCallback, useEffect, useRef, useState } from 'react'
import { ActivityIcon, RefreshCwIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { cn } from 'cn'

type Health =
  | { state: 'checking' }
  | { state: 'connected' }
  | { state: 'degraded' }
  | { state: 'error'; detail: string }

type HealthBody = { status?: unknown; database?: unknown }

const healthEndpoint = '/api/v1/health'

function statusOf(health: Health) {
  switch (health.state) {
    case 'checking':
      return { label: 'Checking', detail: 'Checking the backend and database connection…' }
    case 'connected':
      return { label: 'Connected', detail: 'Backend and database are connected.' }
    case 'degraded':
      return {
        label: 'Degraded',
        detail: 'Backend is running, but its database is unavailable.',
      }
    case 'error':
      return { label: 'Unreachable', detail: health.detail }
  }
}

function variantOf(state: Health['state']) {
  if (state === 'connected') return 'default' as const
  if (state === 'checking') return 'secondary' as const
  return 'destructive' as const
}

const chipTones: Record<Health['state'], string> = {
  checking: 'border-border bg-muted/60 text-muted-foreground',
  connected: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300',
  degraded: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  error: 'border-destructive/30 bg-destructive/10 text-destructive',
}

function useBackendHealth() {
  const [health, setHealth] = useState<Health>({ state: 'checking' })
  const activeRequest = useRef<AbortController>(null)

  const check = useCallback(async () => {
    activeRequest.current?.abort()
    const request = new AbortController()
    activeRequest.current = request

    try {
      const response = await fetch(healthEndpoint, {
        headers: { Accept: 'application/json' },
        cache: 'no-store',
        signal: request.signal,
      })
      const body = (await response.json().catch(() => null)) as HealthBody | null
      if (request.signal.aborted) return

      if (response.status === 200 && body?.status === 'ok' && body.database === 'connected') {
        setHealth({ state: 'connected' })
      } else if (
        response.status === 503 &&
        body?.status === 'degraded' &&
        body.database === 'unavailable'
      ) {
        setHealth({ state: 'degraded' })
      } else if (response.status === 502 || response.status === 504) {
        setHealth({
          state: 'error',
          detail: `The backend could not be reached (HTTP ${response.status}).`,
        })
      } else {
        setHealth({
          state: 'error',
          detail: `Unexpected response from the backend (HTTP ${response.status}).`,
        })
      }
    } catch {
      if (request.signal.aborted) return
      setHealth({
        state: 'error',
        detail: 'The backend could not be reached. Confirm the API server is running, then retry.',
      })
    }
  }, [])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void check()
    return () => activeRequest.current?.abort()
  }, [check])

  const retry = () => {
    setHealth({ state: 'checking' })
    void check()
  }

  return { health, status: statusOf(health), retry }
}

export function BackendStatusChip() {
  const { health, status, retry } = useBackendHealth()

  return (
    <div role="status" aria-live="polite">
      <button
        type="button"
        onClick={retry}
        title={`${status.label}: ${status.detail}`}
        aria-label={`Backend ${status.label.toLowerCase()}. ${status.detail} Activate to re-check.`}
        className={cn(
          'group inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium transition-colors',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
          chipTones[health.state],
        )}
      >
        <span
          aria-hidden="true"
          className={cn(
            'size-1.5 rounded-full bg-current',
            health.state === 'checking' && 'animate-pulse motion-reduce:animate-none',
          )}
        />
        <span className="hidden sm:inline">{status.label}</span>
        <RefreshCwIcon
          aria-hidden="true"
          className="size-3 opacity-0 transition-opacity group-hover:opacity-70 motion-reduce:transition-none"
        />
      </button>
    </div>
  )
}

export function BackendStatus() {
  const { health, status, retry } = useBackendHealth()

  return (
    <Card className="shadow-none">
      <CardHeader className="border-b border-border">
        <CardTitle className="flex items-center gap-2">
          <ActivityIcon className="size-4 text-muted-foreground" aria-hidden="true" />
          Backend connection
        </CardTitle>
        <CardDescription>Server and database availability.</CardDescription>
      </CardHeader>
      <CardContent className="gap-4">
        <div className="flex flex-wrap items-center gap-3" role="status">
          <Badge variant={variantOf(health.state)}>{status.label}</Badge>
          <p className="text-sm text-muted-foreground">{status.detail}</p>
        </div>
        <div>
          <Button type="button" variant="outline" size="sm" onClick={retry}>
            <RefreshCwIcon data-icon="inline-start" />
            Retry
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
