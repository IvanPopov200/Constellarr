import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { CheckCircle2, HelpCircle, LoaderCircle, Plus, RefreshCw, Sparkles, WandSparkles } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { EmptyNote, ErrorNote, Field, LoadingNote, Notice, Choice, Toggle } from '@/components/requests-shared'
import { errorMessage } from '@/lib/api'
import { useAuth } from '@/lib/auth-context'
import {
  approverOptions,
  discoveryApi,
  mediaTypeLabel,
  relativeAge,
  type ApproverOptions,
  type MediaType,
  type Permissions,
  type Recommendation,
  type RecommendationCandidate,
} from '@/lib/discovery-api'

export function RecommendationsPanel() {
  const { can } = useAuth()
  const [history, setHistory] = useState<Recommendation[]>([])
  const [types, setTypes] = useState<MediaType[]>([])
  const [permissions, setPermissions] = useState<Permissions>({ approve: false, requestsWrite: false, libraryWrite: false })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState('')
  const [mediaType, setMediaType] = useState('any')
  const [useHistory, setUseHistory] = useState(true)
  const [titles, setTitles] = useState('')
  const [genres, setGenres] = useState('')
  const [count, setCount] = useState(5)
  const [latest, setLatest] = useState<Recommendation | null>(null)
  const [options, setOptions] = useState<ApproverOptions>({ profiles: [], movieRoots: [], tvRoots: [] })
  const [profileId, setProfileId] = useState('')
  const [rootId, setRootId] = useState('')

  const reload = useCallback(async () => {
    try {
      const loaded = await discoveryApi.recommendations()
      setHistory(loaded.recommendations ?? [])
      setTypes(loaded.types ?? [])
      setPermissions(loaded.permissions ?? { approve: false, requestsWrite: false, libraryWrite: false })
      setError('')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    discoveryApi
      .recommendations(controller.signal)
      .then((loaded) => {
        setHistory(loaded.recommendations ?? [])
        setTypes(loaded.types ?? [])
        setPermissions(loaded.permissions ?? { approve: false, requestsWrite: false, libraryWrite: false })
        setError('')
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [])

  useEffect(() => {
    if (!permissions.libraryWrite) return
    const controller = new AbortController()
    approverOptions(controller.signal).then(setOptions).catch(() => undefined)
    return () => controller.abort()
  }, [permissions.libraryWrite])

  async function generate(event: FormEvent) {
    event.preventDefault()
    setBusy('generate')
    setError('')
    setNotice('')
    try {
      const recommendation = await discoveryApi.generate({
        mediaType,
        useHistory,
        titles: titles.split(',').map((value) => value.trim()).filter(Boolean),
        genres: genres.split(',').map((value) => value.trim()).filter(Boolean),
        count,
      })
      setLatest(recommendation)
      setNotice(
        recommendation.candidates && recommendation.candidates.length > 0
          ? 'Suggestions ready. Accept one to request it or add it to the library.'
          : 'The model returned no usable candidates. Adjust the taste summary and try again.',
      )
      await reload()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy('')
    }
  }

  async function accept(recommendation: Recommendation, candidate: RecommendationCandidate, index: number, action: 'request' | 'add') {
    setBusy(`${recommendation.id}-${index}-${action}`)
    setError('')
    setNotice('')
    try {
      const result = await discoveryApi.accept(recommendation.id, {
        candidate: index,
        action,
        profileId: profileId || undefined,
        rootId: rootId || undefined,
        monitored: true,
      })
      const label = candidate.title
      setNotice(
        action === 'add'
          ? `Added ${label} to the library.`
          : result.request?.status === 'available'
            ? `${label} is already in the library.`
            : `Requested ${label}. Track it on the Requests page.`,
      )
      if (latest?.id === recommendation.id) setLatest(result.recommendation)
      await reload()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy('')
    }
  }

  const mediaOptions = types.length > 0 ? types : (['movie', 'tv'] as MediaType[])

  return (
    <div className="space-y-5">
      <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle className="flex flex-wrap items-center justify-between gap-2">
            <span className="flex items-center gap-2">
              <Sparkles className="size-4 text-muted-foreground" aria-hidden="true" />
              Recommendations
            </span>
            <Badge variant="outline">Optional</Badge>
          </CardTitle>
          <CardDescription>
            The configured OpenAI-compatible model suggests titles from the taste summary you choose. Every suggestion is
            checked against the metadata provider, and unconfirmed titles are marked as unverified.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={generate}>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Media type">
                <Choice value={mediaType} onChange={(event) => setMediaType(event.target.value)}>
                  <option value="any">Any configured type</option>
                  {mediaOptions.map((type) => (
                    <option key={type} value={type}>
                      {mediaTypeLabel(type)}
                    </option>
                  ))}
                </Choice>
              </Field>
              <Field label="Suggestions" hint="Between 1 and 10.">
                <Input type="number" min={1} max={10} value={count} onChange={(event) => setCount(Number(event.target.value) || 1)} />
              </Field>
              <Field label="Titles you like" hint="Comma separated, up to 10.">
                <Input value={titles} onChange={(event) => setTitles(event.target.value)} placeholder="The Matrix, Dune" />
              </Field>
              <Field label="Genres" hint="Comma separated, up to 10.">
                <Input value={genres} onChange={(event) => setGenres(event.target.value)} placeholder="Science Fiction, Thriller" />
              </Field>
            </div>
            <Toggle
              id="use-history"
              label="Use my request history"
              description="Only your own requests are summarized; other users' data never leaves the server."
              checked={useHistory}
              onChange={setUseHistory}
            />
            {permissions.libraryWrite && (
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Quality profile for direct adds" hint="Used when you add a suggestion straight to the library.">
                  <Choice value={profileId} onChange={(event) => setProfileId(event.target.value)}>
                    <option value="">Library default</option>
                    {options.profiles.map((profile) => (
                      <option key={profile.id} value={profile.id}>
                        {profile.name}
                      </option>
                    ))}
                  </Choice>
                </Field>
                <Field label="Root folder for direct adds">
                  <Choice value={rootId} onChange={(event) => setRootId(event.target.value)}>
                    <option value="">Library default</option>
                    {[...options.movieRoots, ...options.tvRoots].map((root) => (
                      <option key={root.id} value={root.id}>
                        {root.id} — {root.path}
                      </option>
                    ))}
                  </Choice>
                </Field>
              </div>
            )}
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-xs text-muted-foreground">
                {can('settings.read') ? <>
                  Configure the AI provider in{' '}
                  <a className="underline underline-offset-4" href="#connections">Connections</a>.
                </> : 'An administrator configures the AI provider for your server.'}
              </p>
              <Button type="submit" size="sm" disabled={busy !== ''}>
                {busy === 'generate' ? <LoaderCircle className="animate-spin" /> : <WandSparkles />}
                Suggest titles
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {error && <ErrorNote>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}
      {loading && <LoadingNote>Loading recommendations…</LoadingNote>}

      {latest && (
        <section className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="font-heading text-sm font-semibold">Latest suggestions</h2>
            <p className="text-xs text-muted-foreground">
              {latest.model} · {relativeAge(latest.createdAt)}
            </p>
          </div>
          {(latest.warnings ?? []).map((warning) => (
            <p key={warning} role="status" className="rounded-md border border-amber-500/20 bg-amber-500/5 px-3 py-2 text-xs text-amber-300">
              {warning}
            </p>
          ))}
          {(latest.candidates ?? []).length === 0 && <EmptyNote>The model returned no usable candidates.</EmptyNote>}
          <ul className="space-y-2">
            {(latest.candidates ?? []).map((candidate, index) => (
              <CandidateRow
                key={`${candidate.title}-${index}`}
                candidate={candidate}
                index={index}
                busy={busy}
                permissions={permissions}
                recommendationId={latest.id}
                onAccept={(action) => void accept(latest, candidate, index, action)}
              />
            ))}
          </ul>
        </section>
      )}

      <section className="space-y-3">
        <h2 className="font-heading text-sm font-semibold">Recent runs</h2>
        {history.length === 0 && !loading && <EmptyNote>No recommendation runs yet.</EmptyNote>}
        <ul className="space-y-2">
          {history.slice(0, 5).map((recommendation) => (
            <li key={recommendation.id} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-3 py-2 text-sm">
              <span className="min-w-0">
                <span className="block truncate">
                  {recommendation.input.genres?.length
                    ? recommendation.input.genres.join(', ')
                    : recommendation.input.titles?.join(', ') || 'Request history'}
                </span>
                <span className="text-xs text-muted-foreground">
                  {recommendation.model} · {relativeAge(recommendation.createdAt)} · {recommendation.candidates?.length ?? 0} candidates
                </span>
              </span>
              <span className="flex items-center gap-2">
                {recommendation.acceptedAction === 'add' && <Badge variant="outline">Added to library</Badge>}
                {recommendation.acceptedAction === 'request' && <Badge variant="outline">Requested</Badge>}
                <Button size="sm" variant="ghost" onClick={() => setLatest(recommendation)}>
                  View
                </Button>
              </span>
            </li>
          ))}
        </ul>
        {history.length > 0 && (
          <Button size="sm" variant="ghost" onClick={() => void reload()} disabled={loading}>
            {loading ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}
            Refresh
          </Button>
        )}
      </section>
    </div>
  )
}

function CandidateRow({ candidate, recommendationId, index, busy, permissions, onAccept }: {
  candidate: RecommendationCandidate
  recommendationId: string
  index: number
  busy: string
  permissions: Permissions
  onAccept: (action: 'request' | 'add') => void
}) {
  const actionBusy = (action: 'request' | 'add') => busy === `${recommendationId}-${index}-${action}`
  const addBusy = actionBusy('add')
  const requestBusy = actionBusy('request')
  return (
    <li className="rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
            {candidate.title}
            {candidate.year > 0 && <span className="text-xs text-muted-foreground">{candidate.year}</span>}
            <Badge variant="ghost" className="text-muted-foreground">{mediaTypeLabel(candidate.mediaType)}</Badge>
            {candidate.verified ? (
              <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 text-emerald-300">
                <CheckCircle2 aria-hidden="true" />
                Verified {candidate.providerId}
              </Badge>
            ) : (
              <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 text-amber-300">
                <HelpCircle aria-hidden="true" />
                Unverified
              </Badge>
            )}
          </p>
          {candidate.reason && <p className="text-sm text-muted-foreground">{candidate.reason}</p>}
          {!candidate.verified && <p className="text-xs text-muted-foreground">{candidate.verification}</p>}
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          {permissions.requestsWrite && (
            <Button
              size="sm"
              variant="outline"
              disabled={!candidate.verified || candidate.accepted || requestBusy}
              title={candidate.verified ? undefined : 'Only titles confirmed by the metadata provider can be requested.'}
              onClick={() => onAccept('request')}
            >
              {requestBusy ? <LoaderCircle className="animate-spin" /> : <Plus />}
              Request
            </Button>
          )}
          {permissions.libraryWrite && (
            <Button
              size="sm"
              disabled={!candidate.verified || candidate.accepted || addBusy}
              title={candidate.verified ? undefined : 'Only titles confirmed by the metadata provider can be added.'}
              onClick={() => onAccept('add')}
            >
              {addBusy ? <LoaderCircle className="animate-spin" /> : <Plus />}
              Add to library
            </Button>
          )}
        </div>
      </div>
      {candidate.accepted && <p className="mt-2 text-xs text-emerald-400">Accepted.</p>}
    </li>
  )
}
