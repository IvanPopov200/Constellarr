import { useState, type FormEvent } from 'react'
import { LoaderCircle, Search, Send } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DiscoveryDialog, EmptyNote, ErrorNote, Field, LoadingNote } from '@/components/requests-shared'
import { cn } from 'cn'
import { errorMessage } from '@/lib/api'
import {
  discoveryApi,
  mediaTypeLabel,
  type DiscoverResult,
  type MediaRequest,
  type MediaType,
} from '@/lib/discovery-api'

type Props = {
  types: MediaType[]
  initialType?: MediaType
  onClose: () => void
  onCreated: (request: MediaRequest) => void
}

export function NewRequestDialog({ types, initialType, onClose, onCreated }: Props) {
  const available = types.length > 0 ? types : (['movie', 'tv'] as MediaType[])
  const [mediaType, setMediaType] = useState<MediaType>(initialType ?? available[0])
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<DiscoverResult[] | null>(null)
  const [selected, setSelected] = useState<DiscoverResult | null>(null)
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState<'search' | 'submit' | null>(null)
  const [error, setError] = useState('')

  async function search(event: FormEvent) {
    event.preventDefault()
    const trimmed = query.trim()
    if (!trimmed) {
      setError('Enter a title to search.')
      return
    }
    setBusy('search')
    setError('')
    setSelected(null)
    try {
      const found = await discoveryApi.discover(mediaType, trimmed)
      setResults(found.results ?? [])
    } catch (cause) {
      setResults(null)
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!selected) return
    setBusy('submit')
    setError('')
    try {
      const request = await discoveryApi.createRequest({
        mediaType,
        providerId: selected.providerId,
        title: selected.title,
        year: selected.year,
        poster: selected.poster,
        message: message.trim(),
      })
      onCreated(request)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  return (
    <DiscoveryDialog
      active
      size="wide"
      title="New request"
      description="Search the metadata provider, then submit the title for review."
      onClose={onClose}
    >
      <form className="space-y-4" onSubmit={search}>
        <div className="flex flex-wrap gap-1.5" role="tablist" aria-label="Media type">
          {available.map((type) => (
            <Button
              key={type}
              type="button"
              role="tab"
              aria-selected={mediaType === type}
              size="sm"
              variant={mediaType === type ? 'secondary' : 'ghost'}
              onClick={() => {
                setMediaType(type)
                setResults(null)
                setSelected(null)
              }}
            >
              {mediaTypeLabel(type)}
            </Button>
          ))}
        </div>
        <div className="flex gap-2">
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={mediaType === 'music' ? 'Search albums or artists' : 'Search titles'}
            aria-label="Search titles"
          />
          <Button type="submit" variant="outline" disabled={busy !== null}>
            {busy === 'search' ? <LoaderCircle className="animate-spin" /> : <Search />}
            Search
          </Button>
        </div>
      </form>

      <div className="mt-4 space-y-2">
        {busy === 'search' && <LoadingNote>Searching the metadata provider…</LoadingNote>}
        {results !== null && results.length === 0 && <EmptyNote>No titles matched. Try a different spelling or release year.</EmptyNote>}
        {results !== null && results.length > 0 && (
          <ul className="space-y-1.5">
            {results.map((result) => (
              <li key={`${result.mediaType}-${result.providerId}`}>
                <button
                  type="button"
                  onClick={() => setSelected(result)}
                  aria-pressed={selected?.providerId === result.providerId}
                  className={cn(
                    'flex w-full items-center gap-3 rounded-lg border px-3 py-2 text-left transition-colors',
                    selected?.providerId === result.providerId ? 'border-primary/60 bg-primary/5' : 'border-border hover:bg-muted/40',
                  )}
                >
                  {result.poster ? (
                    <img src={result.poster} alt="" loading="lazy" className="h-12 w-8 shrink-0 rounded object-cover" />
                  ) : (
                    <span className="h-12 w-8 shrink-0 rounded bg-muted" aria-hidden="true" />
                  )}
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">{result.title}</span>
                    <span className="block text-xs text-muted-foreground">
                      {result.year > 0 ? `${result.year} · ` : ''}
                      {result.providerId}
                    </span>
                  </span>
                  <Badge variant="outline" className="text-muted-foreground">
                    {mediaTypeLabel(result.mediaType)}
                  </Badge>
                </button>
              </li>
            ))}
          </ul>
        )}
        {error && <ErrorNote>{error}</ErrorNote>}
      </div>

      {selected && (
        <form className="mt-5 space-y-4 border-t border-border pt-4" onSubmit={submit}>
          <p className="text-sm">
            Requesting <span className="font-medium">{selected.title}</span>
            {selected.year > 0 && <span className="text-muted-foreground"> ({selected.year})</span>}
          </p>
          <Field label="Message for the approver" hint="Optional, plain text. Up to 1000 characters.">
            <textarea
              value={message}
              onChange={(event) => setMessage(event.target.value)}
              rows={3}
              maxLength={1000}
              className="w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/30"
              placeholder="Anything the approver should know?"
            />
          </Field>
          <p className="text-xs text-muted-foreground">
            The request tracks the download and import automatically, and you can follow up with comments.
          </p>
          {error && <ErrorNote>{error}</ErrorNote>}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy !== null}>
              {busy === 'submit' ? <LoaderCircle className="animate-spin" /> : <Send />}
              Submit request
            </Button>
          </div>
        </form>
      )}
    </DiscoveryDialog>
  )
}
