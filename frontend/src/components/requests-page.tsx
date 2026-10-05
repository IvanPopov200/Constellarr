import { useCallback, useEffect, useMemo, useState } from 'react'
import { ExternalLink, Inbox, LoaderCircle, Plus, RefreshCw, Search, ThumbsUp } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { DeliveryProgress, EmptyNote, ErrorNote, LoadingNote, MediaBadge, Notice, RequestStatusBadge, Choice } from '@/components/requests-shared'
import { NewRequestDialog } from '@/components/requests-new-dialog'
import { RequestDetailDialog } from '@/components/requests-detail-dialog'
import { errorMessage } from '@/lib/api'
import { useAuth } from '@/lib/auth-context'
import {
  catalogHref,
  catalogLabel,
  discoveryApi,
  displayName,
  mediaTypeLabel,
  relativeAge,
  type MediaRequest,
  type RequestList,
} from '@/lib/discovery-api'

const statusFilters = [
  { value: 'open', label: 'Open' },
  { value: 'all', label: 'All' },
  { value: 'pending', label: 'Pending' },
  { value: 'approved', label: 'Approved' },
  { value: 'available', label: 'Available' },
  { value: 'rejected', label: 'Rejected' },
  { value: 'cancelled', label: 'Cancelled' },
]

const openStatuses = new Set(['pending', 'approving', 'approved'])

export function RequestsPage() {
  const { user } = useAuth()
  const currentUserId = user?.id ?? null
  const [list, setList] = useState<RequestList | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [creating, setCreating] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const [approving, setApproving] = useState('')
  const [statusFilter, setStatusFilter] = useState('open')
  const [typeFilter, setTypeFilter] = useState('')
  const [query, setQuery] = useState('')

  const reload = useCallback(async () => {
    try {
      const loaded = await discoveryApi.listRequests()
      setList(loaded)
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
      .listRequests(controller.signal)
      .then((loaded) => {
        setList(loaded)
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

  const requests = useMemo(() => list?.requests ?? [], [list])
  const types = useMemo(() => list?.types ?? [], [list])
  const canApprove = Boolean(list?.canApprove)
  const permissions = list?.permissions ?? { approve: false, requestsWrite: false, libraryWrite: false }

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    return requests.filter((request) => {
      if (statusFilter === 'open' ? !openStatuses.has(request.status) : statusFilter !== 'all' && request.status !== statusFilter) return false
      if (typeFilter && request.mediaType !== typeFilter) return false
      if (!needle) return true
      const requester = displayName(request.userId, request.userName, currentUserId, 'Unknown user')
      const decider = displayName(request.decidedBy, request.decidedByName, currentUserId, 'Unknown user')
      return [request.title, request.providerId, request.userId, requester, decider, request.message].some((value) =>
        value.toLowerCase().includes(needle),
      )
    })
  }, [requests, statusFilter, typeFilter, query, currentUserId])

  const counts = useMemo(() => {
    const summary = { open: 0, available: 0, rejected: 0 }
    for (const request of requests) {
      if (openStatuses.has(request.status)) summary.open++
      if (request.status === 'available') summary.available++
      if (request.status === 'rejected') summary.rejected++
    }
    return summary
  }, [requests])

  async function quickApprove(request: MediaRequest) {
    setApproving(request.id)
    setError('')
    setNotice('')
    try {
      await discoveryApi.approveRequest(request.id, { monitored: true })
      setNotice(`Approved and monitored ${request.title}. The library now searches for releases.`)
      await reload()
    } catch (cause) {
      setError(errorMessage(cause))
      await reload()
    } finally {
      setApproving('')
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div className="space-y-1">
          <h1 className="text-2xl font-semibold tracking-tight">Requests</h1>
          <p className="text-sm text-muted-foreground">
            {canApprove
              ? 'Review every request, then approve with the library destination or reject with a reason.'
              : 'Ask for a movie, show, or album and follow it until it is available in the library.'}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => void reload()} disabled={loading}>
            {loading ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}
            Refresh
          </Button>
          <Button size="sm" onClick={() => setCreating(true)} disabled={types.length === 0}>
            <Plus />
            New request
          </Button>
        </div>
      </header>

      {error && <ErrorNote onRetry={() => void reload()}>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}

      {types.length === 0 && !loading && (
        <EmptyNote>No media type is configured yet. Connect a metadata provider or the music library first.</EmptyNote>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <span className="flex flex-wrap items-center gap-1.5 text-sm text-muted-foreground">
          <Badge variant="outline">{counts.open} open</Badge>
          <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/10 text-emerald-300">
            {counts.available} available
          </Badge>
          {counts.rejected > 0 && <Badge variant="outline">{counts.rejected} rejected</Badge>}
        </span>
        <span className="flex flex-1 flex-wrap items-center justify-end gap-2">
          <Choice value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)} aria-label="Filter by status">
            {statusFilters.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </Choice>
          <Choice value={typeFilter} onChange={(event) => setTypeFilter(event.target.value)} aria-label="Filter by media type">
            <option value="">All media</option>
            {types.map((type) => (
              <option key={type} value={type}>
                {mediaTypeLabel(type)}
              </option>
            ))}
          </Choice>
          <div className="relative">
            <Search className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search requests"
              aria-label="Search requests"
              className="w-56 pl-8"
            />
          </div>
        </span>
      </div>

      {loading && requests.length === 0 && <LoadingNote>Loading requests…</LoadingNote>}
      {!loading && requests.length === 0 && (
        <Card className="shadow-none">
          <CardContent className="items-start gap-3 py-6">
            <Inbox className="size-5 text-muted-foreground" aria-hidden="true" />
            <p className="text-sm text-muted-foreground">
              Nothing requested yet. Search for a title and the approver queue takes it from there.
            </p>
            <Button size="sm" variant="outline" onClick={() => setCreating(true)} disabled={types.length === 0}>
              <Plus />
              New request
            </Button>
          </CardContent>
        </Card>
      )}
      {!loading && requests.length > 0 && filtered.length === 0 && <EmptyNote>No requests match these filters.</EmptyNote>}

      <ul className="space-y-3">
        {filtered.map((request) => {
          const catalog = catalogHref(request.mediaType, request.libraryId)
          return (
            <li key={request.id}>
              <Card className="shadow-none">
                <CardContent className="gap-3">
                  <div className="flex flex-wrap items-start gap-3">
                    {request.poster ? (
                      <img src={request.poster} alt="" loading="lazy" className="h-20 w-14 shrink-0 rounded object-cover" />
                    ) : (
                      <span className="h-20 w-14 shrink-0 rounded bg-muted" aria-hidden="true" />
                    )}
                    <div className="min-w-0 flex-1 space-y-1.5">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-medium">{request.title}</span>
                        {request.year > 0 && <span className="text-xs text-muted-foreground">{request.year}</span>}
                        <RequestStatusBadge status={request.status} />
                        <MediaBadge type={request.mediaType} />
                      </div>
                      <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                        <span>{displayName(request.userId, request.userName, currentUserId, 'Unknown user')}</span>
                        <span>{relativeAge(request.createdAt)}</span>
                        <span className="font-mono">{request.providerId}</span>
                      </p>
                      {request.message && <p className="text-sm">{request.message}</p>}
                      {request.decisionNote && (
                        <p className="text-xs text-muted-foreground">
                          {request.decidedBy
                            ? `${displayName(request.decidedBy, request.decidedByName, currentUserId, 'Unknown user')}: `
                            : ''}
                          {request.decisionNote}
                        </p>
                      )}
                      {(request.status === 'approved' || request.status === 'available') && <DeliveryProgress delivery={request.delivery} />}
                    </div>
                    <div className="flex shrink-0 flex-wrap items-center gap-2">
                      {catalog && (
                        <Button asChild size="sm" variant="ghost">
                          <a href={catalog}>
                            <ExternalLink data-icon="inline-start" />
                            {catalogLabel(request.mediaType)}
                          </a>
                        </Button>
                      )}
                      <Button size="sm" variant="outline" onClick={() => setSelected(request.id)}>
                        Details
                      </Button>
                      {canApprove && request.status === 'pending' && (
                        <Button
                          size="sm"
                          title="Approve with monitoring; use Details to pick a profile, root folder, or turn monitoring off."
                          disabled={approving === request.id}
                          onClick={() => void quickApprove(request)}
                        >
                          {approving === request.id ? <LoaderCircle className="animate-spin" /> : <ThumbsUp />}
                          Approve
                        </Button>
                      )}
                    </div>
                  </div>
                </CardContent>
              </Card>
            </li>
          )
        })}
      </ul>

      {creating && (
        <NewRequestDialog
          types={types}
          onClose={() => setCreating(false)}
          onCreated={(request) => {
            setCreating(false)
            setNotice(`Request submitted: ${request.title}. It is now ${request.status === 'available' ? 'already available' : 'waiting for review'}.`)
            setSelected(request.id)
            void reload()
          }}
        />
      )}
      {selected && (
        <RequestDetailDialog
          id={selected}
          canApprove={canApprove}
          permissions={permissions}
          onClose={() => setSelected(null)}
          onChanged={() => void reload()}
        />
      )}
    </div>
  )
}
