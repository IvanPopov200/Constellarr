import { useState } from 'react'
import type { FormEvent } from 'react'
import { SearchIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { formatAge, formatBytes } from '@/lib/format'
import { torrentsApi, type TorrentSearchResult, type TorrentSource } from '@/lib/torrents-api'

type Props = { sources: TorrentSource[]; canAdd: boolean; canReadSources: boolean; onAdded: () => void }

export function TorrentSearch({ sources, canAdd, canReadSources, onAdded }: Props) {
  const [query, setQuery] = useState('')
  const [sourceId, setSourceId] = useState('')
  const [results, setResults] = useState<TorrentSearchResult[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const enabled = sources.filter((source) => source.enabled)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!query.trim()) {
      setError('Enter a search term.')
      return
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const response = await torrentsApi.search(query.trim(), sourceId)
      setResults(response.results)
      if (response.errors?.length) {
        setNotice(response.errors.map((entry) => `${entry.source}: ${entry.error}`).join(' · '))
      }
    } catch (cause) {
      setError(errorMessage(cause))
      setResults(null)
    } finally {
      setBusy(false)
    }
  }

  const add = async (result: TorrentSearchResult) => {
    setBusy(true)
    setError(null)
    try {
      await torrentsApi.add({ sourceId: result.sourceId, resultId: result.id })
      setNotice(`Added ${result.title}.`)
      onAdded()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Search indexers</CardTitle>
        <CardDescription>Search the configured Torznab sources and add a result to the queue.</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="flex flex-wrap items-end gap-2" onSubmit={submit}>
          <label className="flex min-w-56 flex-1 flex-col gap-1 text-sm">
            Search term
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Movie, series or release name"
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Source
            <select
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
              value={sourceId}
              onChange={(event) => setSourceId(event.target.value)}
            >
              <option value="">All enabled sources</option>
              {enabled.map((source) => (
                <option key={source.id} value={source.id}>
                  {source.name}
                </option>
              ))}
            </select>
          </label>
          <Button type="submit" disabled={busy}>
            <SearchIcon aria-hidden="true" />
            Search
          </Button>
        </form>

        {canReadSources && enabled.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">
            Add a Torznab source below to search public or private indexers.
          </p>
        ) : null}
        {error ? (
          <p role="alert" className="mt-3 text-sm text-destructive">
            {error}
          </p>
        ) : null}
        {notice ? (
          <p role="status" className="mt-3 text-sm text-muted-foreground">
            {notice}
          </p>
        ) : null}

        {results ? (
          results.length === 0 ? (
            <p className="mt-3 text-sm text-muted-foreground">No releases matched this search.</p>
          ) : (
            <ul className="mt-3 flex flex-col gap-2">
              {results.map((result) => (
                <li
                  key={result.id}
                  className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border px-3 py-2"
                >
                  <div className="flex min-w-0 flex-col">
                    <span className="truncate text-sm">{result.title}</span>
                    <span className="text-xs text-muted-foreground">
                      {formatBytes(result.size)} · {result.seeders >= 0 ? `${result.seeders} seeders` : 'seeders unknown'}
                      {result.leechers >= 0 ? ` · ${result.leechers} leechers` : ''}
                      {result.published ? ` · ${formatAge(result.published)}` : ''}
                    </span>
                  </div>
                  <div className="flex items-center gap-2">
                    <Badge variant="outline">{result.source}</Badge>
                    {result.category ? <Badge variant="ghost">{result.category}</Badge> : null}
                    {canAdd ? (
                      <Button size="sm" disabled={busy} onClick={() => void add(result)}>
                        Add
                      </Button>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>
          )
        ) : null}
      </CardContent>
    </Card>
  )
}
