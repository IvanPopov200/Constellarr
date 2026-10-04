import { useCallback, useEffect, useState } from 'react'
import {
  LoaderCircleIcon,
  RefreshCwIcon,
  SearchIcon,
  ServerIcon,
  SettingsIcon,
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
import { api, errorMessage, type Sources, type SourceTestResult } from '@/lib/api'

function ConfiguredBadge({ configured }: { configured: boolean }) {
  return (
    <Badge variant={configured ? 'default' : 'secondary'}>
      {configured ? 'Configured' : 'Not configured'}
    </Badge>
  )
}

function SourceRow({
  icon: Icon,
  name,
  detail,
  configured,
}: {
  icon: LucideIcon
  name: string
  detail: string
  configured: boolean
}) {
  return (
    <li className="flex items-center justify-between gap-3 py-2.5 first:pt-0 last:pb-0">
      <div className="flex min-w-0 items-center gap-2.5">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-md border border-border bg-background/60 text-muted-foreground">
          <Icon className="size-3.5" aria-hidden="true" />
        </span>
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{name}</p>
          <p className="truncate text-xs text-muted-foreground">{detail}</p>
        </div>
      </div>
      <ConfiguredBadge configured={configured} />
    </li>
  )
}

function TestResult({ label, result }: { label: string; result: SourceTestResult }) {
  return (
    <li className="flex flex-wrap items-center gap-2">
      <span className="font-medium">{label}</span>
      <Badge variant={result.ok ? 'default' : 'destructive'}>
        {result.ok ? 'Connected' : 'Failed'}
      </Badge>
      {!result.ok && result.error && <span className="text-muted-foreground">{result.error}</span>}
    </li>
  )
}

export function SourcesCard() {
  const [sources, setSources] = useState<Sources | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<{ indexer: SourceTestResult; usenet: SourceTestResult } | null>(
    null,
  )
  const [testError, setTestError] = useState<string | null>(null)

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      setSources(await api.getSources(signal))
      setLoadError(null)
    } catch (cause) {
      if (signal?.aborted) return
      setLoadError(errorMessage(cause))
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    setTestError(null)
    try {
      setTest(await api.testSources())
    } catch (cause) {
      setTestError(errorMessage(cause))
    } finally {
      setTesting(false)
    }
  }

  const configuredCount = sources
    ? Number(sources.indexer.configured) + Number(sources.usenet.configured)
    : 0

  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          Sources
        </CardTitle>
        <CardDescription>Indexer and Usenet connection status.</CardDescription>
        {sources && (
          <CardAction>
            <Badge variant="outline">{configuredCount} of 2 configured</Badge>
          </CardAction>
        )}
      </CardHeader>
      <CardContent>
        {sources === null && !loadError && (
          <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" />
            Loading source configuration…
          </p>
        )}

        {loadError && (
          <div className="flex flex-col gap-2">
            <p role="alert" className="text-sm text-destructive">
              {loadError}
            </p>
            <div>
              <Button size="sm" variant="outline" onClick={() => void load()}>
                <RefreshCwIcon data-icon="inline-start" />
                Retry
              </Button>
            </div>
          </div>
        )}

        {sources && (
          <ul className="flex flex-col divide-y divide-border">
            <SourceRow
              icon={SearchIcon}
              name={sources.indexer.name}
              detail="Indexer"
              configured={sources.indexer.configured}
            />
            <SourceRow
              icon={ServerIcon}
              name={sources.usenet.name}
              detail={
                sources.usenet.host
                  ? `${sources.usenet.host}:${sources.usenet.port} · ${sources.usenet.connections} connections`
                  : 'No host configured'
              }
              configured={sources.usenet.configured}
            />
          </ul>
        )}

        <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border bg-background/50 px-3 py-2.5">
          <p className="min-w-0 flex-1 text-xs text-muted-foreground">
            Sources are configured in Settings. Credentials stay on the server and are never
            entered in the browser.
          </p>
          <Button asChild size="sm" variant="outline">
            <a href="#settings">
              <SettingsIcon data-icon="inline-start" />
              Open settings
            </a>
          </Button>
        </div>

        <div>
          <Button size="sm" variant="outline" disabled={testing} onClick={() => void runTest()}>
            {testing && (
              <LoaderCircleIcon
                data-icon="inline-start"
                className="animate-spin motion-reduce:animate-none"
              />
            )}
            {testing ? 'Testing…' : 'Test connections'}
          </Button>
        </div>

        {testError && (
          <p role="alert" className="text-sm text-destructive">
            {testError}
          </p>
        )}

        {test && (
          <div role="status">
            <ul className="flex flex-col gap-2 text-sm">
              <TestResult label={sources?.indexer.name ?? 'Indexer'} result={test.indexer} />
              <TestResult label={sources?.usenet.name ?? 'Usenet'} result={test.usenet} />
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
