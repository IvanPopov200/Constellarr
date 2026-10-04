import { useCallback, useEffect, useRef, useState } from 'react'
import { RefreshCwIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

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

export function BackendStatus() {
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

  const status = statusOf(health)
  const variant =
    health.state === 'connected'
      ? 'default'
      : health.state === 'checking'
        ? 'secondary'
        : 'destructive'

  return (
    <Card>
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          Backend connection
        </CardTitle>
        <CardDescription>Live status from {healthEndpoint}.</CardDescription>
      </CardHeader>
      <CardContent className="gap-4">
        <div className="flex flex-wrap items-center gap-3" role="status">
          <Badge variant={variant}>{status.label}</Badge>
          <p className="text-sm text-muted-foreground">{status.detail}</p>
        </div>
        <div>
          <Button variant="outline" size="sm" onClick={retry}>
            <RefreshCwIcon data-icon="inline-start" />
            Retry
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
