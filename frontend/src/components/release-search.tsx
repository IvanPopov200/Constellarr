import { useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { DownloadIcon, LoaderCircleIcon, RefreshCwIcon, SearchIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { api, errorMessage, type Job, type Release } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'

type SearchState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'error'; message: string; query: string }
  | { status: 'ready'; query: string; releases: Release[] }

function jobActionLabel(status: Job['status'] | undefined) {
  if (!status) return null
  if (status === 'failed') return 'Failed — retry in queue'
  if (status === 'completed') return 'Completed'
  return 'In queue'
}

export function ReleaseSearch({
  jobs,
  onDownload,
}: {
  jobs: Job[]
  onDownload: (release: Release) => Promise<void>
}) {
  const [query, setQuery] = useState('')
  const [state, setState] = useState<SearchState>({ status: 'idle' })
  const [pendingId, setPendingId] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const requestId = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const jobStatus = useMemo(() => new Map(jobs.map((job) => [job.releaseId, job.status])), [jobs])

  useEffect(() => () => controller.current?.abort(), [])

  const runSearch = async (term: string) => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    const id = requestId.current + 1
    requestId.current = id
    setState({ status: 'loading' })
    setActionError(null)

    try {
      const releases = await api.searchReleases(term, request.signal)
      if (requestId.current !== id) return
      setState({ status: 'ready', query: term, releases })
    } catch (cause) {
      if (requestId.current !== id || request.signal.aborted) return
      setState({ status: 'error', message: errorMessage(cause), query: term })
    }
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const term = query.trim()
    if (term) void runSearch(term)
  }

  const download = async (release: Release) => {
    setPendingId(release.id)
    setActionError(null)
    try {
      await onDownload(release)
    } catch (cause) {
      setActionError(errorMessage(cause))
    } finally {
      setPendingId(null)
    }
  }

  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          Search releases
        </CardTitle>
        <CardDescription>Search the configured indexer for movie releases.</CardDescription>
      </CardHeader>
      <CardContent aria-busy={state.status === 'loading'}>
        <form className="flex w-full max-w-2xl flex-col gap-2 sm:flex-row" onSubmit={submit}>
          <label className="sr-only" htmlFor="release-search">
            Search releases
          </label>
          <Input
            id="release-search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Movie title, year, or keywords"
            autoComplete="off"
            spellCheck={false}
            maxLength={256}
          />
          <Button type="submit" disabled={!query.trim()}>
            {state.status === 'loading' ? (
              <LoaderCircleIcon
                data-icon="inline-start"
                className="animate-spin motion-reduce:animate-none"
              />
            ) : (
              <SearchIcon data-icon="inline-start" />
            )}
            {state.status === 'loading' ? 'Searching…' : 'Search'}
          </Button>
        </form>

        {state.status === 'idle' && (
          <p className="text-sm text-muted-foreground">Submit a search to see releases.</p>
        )}
        {state.status === 'loading' && (
          <p role="status" className="text-sm text-muted-foreground">
            Searching…
          </p>
        )}
        {state.status === 'error' && (
          <div
            role="alert"
            className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            <span className="min-w-0">{state.message}</span>
            <Button
              size="sm"
              variant="outline"
              className="ml-auto"
              onClick={() => void runSearch(state.query)}
            >
              <RefreshCwIcon data-icon="inline-start" />
              Try again
            </Button>
          </div>
        )}
        {state.status === 'ready' && (
          <p role="status" className="text-sm text-muted-foreground">
            {state.releases.length === 0
              ? `No releases found for “${state.query}”.`
              : `${state.releases.length} ${state.releases.length === 1 ? 'release' : 'releases'} for “${state.query}”.`}
          </p>
        )}

        {actionError && (
          <p role="alert" className="text-sm text-destructive">
            {actionError}
          </p>
        )}

        {state.status === 'ready' && state.releases.length > 0 && (
          <ul className="flex flex-col divide-y divide-border">
            {state.releases.map((release) => {
              const status = jobStatus.get(release.id)
              const label =
                pendingId === release.id ? 'Adding…' : (jobActionLabel(status) ?? 'Download')
              return (
                <li
                  key={release.id}
                  className="flex flex-col gap-2 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between sm:gap-4"
                >
                  <div className="min-w-0">
                    <p className="text-sm font-medium break-words">{release.title}</p>
                    <p className="text-xs text-muted-foreground">
                      {formatBytes(release.size)} ·{' '}
                      <time
                        dateTime={release.published}
                        title={new Date(release.published).toLocaleString()}
                      >
                        {formatAge(release.published)}
                      </time>
                    </p>
                  </div>
                  <Button
                    size="sm"
                    variant={status === 'failed' ? 'outline' : 'default'}
                    className="self-start sm:self-auto"
                    disabled={status !== undefined || pendingId !== null}
                    aria-label={`${label}: ${release.title}`}
                    onClick={() => void download(release)}
                  >
                    {!label.startsWith('Adding') && !status && (
                      <DownloadIcon data-icon="inline-start" />
                    )}
                    {label}
                  </Button>
                </li>
              )
            })}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
