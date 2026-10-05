import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { ExternalLink, LoaderCircle, MessageSquare, ShieldCheck, ThumbsDown, ThumbsUp, XCircle } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { DeliveryProgress, DiscoveryDialog, EmptyNote, ErrorNote, Field, LoadingNote, MediaBadge, Notice, RequestStatusBadge, Choice, Toggle } from '@/components/requests-shared'
import { errorMessage } from '@/lib/api'
import { useAuth } from '@/lib/auth-context'
import {
  approverOptions,
  catalogHref,
  catalogLabel,
  discoveryApi,
  displayName,
  mediaTypeLabel,
  relativeAge,
  requestStatusLabel,
  type ApproverOptions,
  type Permissions,
  type RequestDetail,
} from '@/lib/discovery-api'

type Props = {
  id: string
  canApprove: boolean
  permissions: Permissions
  onClose: () => void
  onChanged: () => void
}

const monitorModes = ['all', 'future', 'missing', 'existing', 'first', 'latest', 'none']

export function RequestDetailDialog({ id, canApprove, permissions, onClose, onChanged }: Props) {
  const { user } = useAuth()
  const currentUserId = user?.id ?? null
  const [detail, setDetail] = useState<RequestDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState<'approve' | 'reject' | 'comment' | 'cancel' | null>(null)
  const [options, setOptions] = useState<ApproverOptions>({ profiles: [], movieRoots: [], tvRoots: [] })
  const [profileId, setProfileId] = useState('')
  const [rootId, setRootId] = useState('')
  const [monitored, setMonitored] = useState(true)
  const [monitorMode, setMonitorMode] = useState('')
  const [note, setNote] = useState('')
  const [rejecting, setRejecting] = useState(false)
  const [reason, setReason] = useState('')
  const [comment, setComment] = useState('')

  const reload = useCallback(async () => {
    try {
      const loaded = await discoveryApi.request(id)
      setDetail(loaded)
      setError('')
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }, [id])

  useEffect(() => {
    const controller = new AbortController()
    discoveryApi
      .request(id, controller.signal)
      .then((loaded) => {
        setDetail(loaded)
        setError('')
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [id])

  useEffect(() => {
    if (!canApprove) return
    const controller = new AbortController()
    approverOptions(controller.signal).then(setOptions).catch(() => undefined)
    return () => controller.abort()
  }, [canApprove])

  async function act(kind: 'approve' | 'reject' | 'comment' | 'cancel') {
    setBusy(kind)
    setError('')
    setNotice('')
    try {
      if (kind === 'approve') {
        const monitoredNow = monitored
        await discoveryApi.approveRequest(id, {
          profileId: profileId || undefined,
          rootId: rootId || undefined,
          monitored: monitoredNow,
          monitorMode: monitorMode || undefined,
          note: note.trim() || undefined,
        })
        setNotice(
          monitoredNow
            ? 'Approved and monitored. The library now searches and imports this title.'
            : 'Approved without monitoring. Nothing is searched or imported until you monitor it.',
        )
        setRejecting(false)
      } else if (kind === 'reject') {
        await discoveryApi.rejectRequest(id, reason.trim())
        setNotice('Rejected. The requester can submit it again later.')
        setRejecting(false)
      } else if (kind === 'comment') {
        await discoveryApi.comment(id, comment.trim())
        setComment('')
        setNotice('Comment added.')
      } else {
        await discoveryApi.cancelRequest(id)
        setNotice('Request cancelled.')
      }
      await reload()
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  const request = detail?.request
  const roots = options.movieRoots.length > 0 ? options.movieRoots : options.tvRoots
  const rootsForType = request?.mediaType === 'tv' ? (options.tvRoots.length > 0 ? options.tvRoots : roots) : roots
  const catalog = request ? catalogHref(request.mediaType, request.libraryId) : ''
  const canCancel = request?.status === 'pending' && !canApprove && permissions.requestsWrite

  return (
    <DiscoveryDialog
      active
      size="wide"
      title={request?.title ?? 'Request'}
      description={
        request
          ? `${mediaTypeLabel(request.mediaType)} · requested by ${displayName(request.userId, request.userName, currentUserId, 'Unknown user')}`
          : 'Loading request'
      }
      onClose={onClose}
    >
      {loading && <LoadingNote>Loading the request…</LoadingNote>}
      {error && <ErrorNote onRetry={() => void reload()}>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}
      {request && (
        <div className="space-y-5">
          <div className="flex flex-wrap items-center gap-2">
            <RequestStatusBadge status={request.status} />
            <MediaBadge type={request.mediaType} />
            {request.year > 0 && <Badge variant="ghost" className="text-muted-foreground">{request.year}</Badge>}
            <Badge variant="ghost" className="font-mono text-[0.7rem] text-muted-foreground">
              {request.providerId}
            </Badge>
            <span className="text-xs text-muted-foreground">created {relativeAge(request.createdAt)}</span>
          </div>

          {(request.message || request.decisionNote) && (
            <div className="space-y-2 rounded-lg border border-border p-3 text-sm">
              {request.message && <p>{request.message}</p>}
              {request.decisionNote && (
                <p className="text-muted-foreground">
                  <span className="font-medium text-foreground">{requestStatusLabel(request.status)}</span>
                  {request.decidedBy
                    ? ` by ${displayName(request.decidedBy, request.decidedByName, currentUserId, 'Unknown user')}`
                    : ''}
                  : {request.decisionNote}
                </p>
              )}
            </div>
          )}

          <div className="space-y-2 rounded-lg border border-border p-3">
            <DeliveryProgress delivery={request.delivery} />
            <p className="text-xs text-muted-foreground">
              {request.libraryId
                ? 'Linked to a catalog entry in your library.'
                : request.status === 'approved' || request.status === 'available'
                  ? 'Waiting for the library to link this request'
                  : 'Not linked to the library yet'}
            </p>
            {catalog && (
              <Button asChild size="sm" variant="outline">
                <a href={catalog}>
                  <ExternalLink data-icon="inline-start" />
                  {catalogLabel(request.mediaType)}
                </a>
              </Button>
            )}
          </div>

          {canApprove && (request.status === 'pending' || request.status === 'approving') && (
            <Card className="shadow-none">
              <CardHeader className="border-b border-border">
                <CardTitle className="flex items-center gap-2">
                  <ShieldCheck className="size-4 text-muted-foreground" aria-hidden="true" />
                  Approver decision
                </CardTitle>
                <CardDescription>Pick the destination, then approve or reject with a reason.</CardDescription>
              </CardHeader>
              <CardContent>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field label="Quality profile" hint="Leave empty to use the library default.">
                    <Choice value={profileId} onChange={(event) => setProfileId(event.target.value)} disabled={request.mediaType === 'music'}>
                      <option value="">Library default</option>
                      {options.profiles.map((profile) => (
                        <option key={profile.id} value={profile.id}>
                          {profile.name}
                        </option>
                      ))}
                    </Choice>
                  </Field>
                  <Field label="Root folder" hint="Leave empty to use the library default.">
                    <Choice value={rootId} onChange={(event) => setRootId(event.target.value)} disabled={request.mediaType === 'music'}>
                      <option value="">Library default</option>
                      {rootsForType.map((root) => (
                        <option key={root.id} value={root.id}>
                          {root.id} — {root.path}
                        </option>
                      ))}
                    </Choice>
                  </Field>
                  {request.mediaType === 'tv' && (
                    <Field label="Monitor mode" hint="Which episodes the library should look for.">
                      <Choice value={monitorMode} onChange={(event) => setMonitorMode(event.target.value)}>
                        <option value="">Library default (all)</option>
                        {monitorModes.map((mode) => (
                          <option key={mode} value={mode}>
                            {mode}
                          </option>
                        ))}
                      </Choice>
                    </Field>
                  )}
                  <Field label="Decision note" hint="Optional, plain text shown to the requester.">
                    <Input value={note} onChange={(event) => setNote(event.target.value)} maxLength={500} placeholder="Approved for the main library" />
                  </Field>
                </div>
                <div className="mt-4">
                  <Toggle
                    id="approve-monitored"
                    label="Monitor this title"
                    description="Approved requests are monitored so the library searches for releases."
                    checked={monitored}
                    onChange={setMonitored}
                  />
                </div>
                {rejecting && (
                  <div className="mt-4 space-y-2">
                    <Field label="Rejection reason" hint="Required. Up to 500 characters.">
                      <Input value={reason} onChange={(event) => setReason(event.target.value)} maxLength={500} placeholder="Already in the library" />
                    </Field>
                  </div>
                )}
                <div className="mt-4 flex flex-wrap justify-end gap-2">
                  {rejecting ? (
                    <>
                      <Button variant="ghost" size="sm" onClick={() => setRejecting(false)}>
                        Keep pending
                      </Button>
                      <Button variant="destructive" size="sm" disabled={busy !== null || !reason.trim()} onClick={() => void act('reject')}>
                        {busy === 'reject' ? <LoaderCircle className="animate-spin" /> : <ThumbsDown />}
                        Confirm rejection
                      </Button>
                    </>
                  ) : (
                    <>
                      <Button variant="outline" size="sm" disabled={busy !== null} onClick={() => setRejecting(true)}>
                        <ThumbsDown />
                        Reject
                      </Button>
                      <Button size="sm" disabled={busy !== null} onClick={() => void act('approve')}>
                        {busy === 'approve' ? <LoaderCircle className="animate-spin" /> : <ThumbsUp />}
                        {request.status === 'approving' ? 'Retry approval' : 'Approve'}
                      </Button>
                    </>
                  )}
                </div>
              </CardContent>
            </Card>
          )}

          {canCancel && (
            <div className="flex justify-end">
              <Button variant="destructive" size="sm" disabled={busy !== null} onClick={() => void act('cancel')}>
                {busy === 'cancel' ? <LoaderCircle className="animate-spin" /> : <XCircle />}
                Cancel request
              </Button>
            </div>
          )}

          <section className="space-y-3">
            <h3 className="flex items-center gap-2 font-heading text-sm font-semibold">
              <MessageSquare className="size-4 text-muted-foreground" aria-hidden="true" />
              Comments
            </h3>
            {(detail?.comments ?? []).length === 0 && <EmptyNote>No comments yet. Add context for the approver or the requester.</EmptyNote>}
            <ul className="space-y-2">
              {(detail?.comments ?? []).map((entry) => (
                <li key={entry.id} className="rounded-lg border border-border p-3 text-sm">
                  <p className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                    <span className="font-medium text-foreground">
                      {displayName(entry.userId, entry.userName, currentUserId, 'Unknown user')}
                    </span>
                    <span>{relativeAge(entry.createdAt)}</span>
                  </p>
                  <p className="mt-1 whitespace-pre-wrap break-words">{entry.body}</p>
                </li>
              ))}
            </ul>
            {permissions.requestsWrite ? (
              <form
                className="flex flex-wrap items-start gap-2"
                onSubmit={(event: FormEvent) => {
                  event.preventDefault()
                  if (comment.trim()) void act('comment')
                }}
              >
                <Input
                  value={comment}
                  onChange={(event) => setComment(event.target.value)}
                  maxLength={1000}
                  placeholder="Add a comment"
                  aria-label="Add a comment"
                  className="min-w-56 flex-1"
                />
                <Button type="submit" size="sm" variant="outline" disabled={busy !== null || !comment.trim()}>
                  {busy === 'comment' ? <LoaderCircle className="animate-spin" /> : <MessageSquare />}
                  Comment
                </Button>
              </form>
            ) : (
              <p className="text-xs text-muted-foreground">Commenting needs the requests.write permission.</p>
            )}
          </section>

          <section className="space-y-3">
            <h3 className="font-heading text-sm font-semibold">Activity</h3>
            <ol className="space-y-2">
              {(detail?.events ?? []).map((event) => (
                <li key={event.id} className="rounded-lg border border-border p-3 text-xs">
                  <p className="flex flex-wrap items-center justify-between gap-2 text-muted-foreground">
                    <span>
                      <span className="font-medium text-foreground">
                        {displayName(event.actor, event.actorName, currentUserId, 'System')}
                      </span>{' '}
                      {event.action}
                      {event.toStatus ? ` → ${requestStatusLabel(event.toStatus)}` : ''}
                    </span>
                    <span>{relativeAge(event.createdAt)}</span>
                  </p>
                  {event.message && <p className="mt-1 break-words">{event.message}</p>}
                </li>
              ))}
            </ol>
          </section>
        </div>
      )}
    </DiscoveryDialog>
  )
}
