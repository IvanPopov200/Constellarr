import { useCallback, useEffect, useState } from 'react'
import { LoaderCircleIcon, RefreshCwIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { api, errorMessage, type Sources, type SourceTestResult } from '@/lib/api'

function ConfiguredBadge({ configured }: { configured: boolean }) {
  return (
    <Badge variant={configured ? 'default' : 'secondary'}>
      {configured ? 'Configured' : 'Not configured'}
    </Badge>
  )
}

function TestResult({ label, result }: { label: string; result: SourceTestResult }) {
  return (
    <li className="flex flex-wrap items-center gap-2">
      <span className="font-medium">{label}</span>
      <Badge variant={result.ok ? 'default' : 'destructive'}>
        {result.ok ? 'Connected' : 'Failed'}
      </Badge>
      {!result.ok && result.error && (
        <span className="text-muted-foreground">{result.error}</span>
      )}
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

  return (
    <Card>
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          Sources
        </CardTitle>
        <CardDescription>Indexer and Usenet connection status.</CardDescription>
      </CardHeader>
      <CardContent>
        {sources === null && !loadError && (
          <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <LoaderCircleIcon className="size-4 animate-spin" />
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
          <ul className="flex flex-col gap-3">
            <li className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium">{sources.indexer.name}</span>
              <ConfiguredBadge configured={sources.indexer.configured} />
            </li>
            <li className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <p className="font-medium">{sources.usenet.name}</p>
                <p className="text-sm text-muted-foreground">
                  {sources.usenet.host
                    ? `${sources.usenet.host}:${sources.usenet.port} · ${sources.usenet.connections} connections`
                    : 'No host configured'}
                </p>
              </div>
              <ConfiguredBadge configured={sources.usenet.configured} />
            </li>
          </ul>
        )}

        <p className="text-sm text-muted-foreground">
          Sources are configured through Constellarr&apos;s local runtime settings. Credentials stay
          on the server and are never entered in the browser.
        </p>

        <div>
          <Button
            size="sm"
            variant="outline"
            disabled={testing}
            onClick={() => void runTest()}
          >
            {testing && <LoaderCircleIcon data-icon="inline-start" className="animate-spin" />}
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
