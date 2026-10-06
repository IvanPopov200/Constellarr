import { useEffect, useRef, useState } from 'react'
import { CircleAlertIcon, DownloadIcon, LoaderCircleIcon, SearchIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DialogShell, EmptyState, ErrorNote, LoadingNote, Notice, Section } from '@/components/tv-ui'
import { errorMessage } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { formatAge, formatBytes } from '@/lib/format'
import { queueHref, queueLabel, strings } from '@/components/tv-shared'
import { tvApi, type Target, type TvRelease } from '@/lib/tv-api'
import { cn } from 'cn'

function targetLabel(target: Target) {
  if (target.season < 0) return 'Whole series'
  if (target.episode > 0) return `S${String(target.season).padStart(2, '0')}E${String(target.episode).padStart(2, '0')}`
  return `Season ${target.season} pack`
}

export function ReleaseDialog({
  active,
  seriesId,
  seriesTitle,
  target,
  onClose,
  onGrabbed,
}: {
  active: boolean
  seriesId: string
  seriesTitle: string
  target: Target
  onClose: () => void
  onGrabbed?: (seriesId: string) => void
}) {
  const [releases, setReleases] = useState<TvRelease[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [grabbing, setGrabbing] = useState('')
  const [confirmId, setConfirmId] = useState('')
  const [error, setError] = useState('')
  const [queued, setQueued] = useState<{ title: string; protocol: string } | null>(null)
  const [notice, setNotice] = useState('')
  const controller = useRef<AbortController | null>(null)
  const { can } = useAuth()
  // The queue pages need downloads.read; without it the link would only redirect to the overview.
  const canReadQueue = can(accessPermissions.downloadsRead)

  useEffect(() => () => controller.current?.abort(), [])

  // Opening the dialog never queries indexers; only an explicit search does.
  const search = () => {
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    setSearching(true)
    setReleases(null)
    setError('')
    setNotice('')
    setQueued(null)
    setConfirmId('')
    tvApi
      .search(seriesId, target, request.signal)
      .then((found) => {
        if (!request.signal.aborted) setReleases(found)
      })
      .catch((cause) => {
        if (!request.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!request.signal.aborted) setSearching(false)
      })
  }

  const grab = async (release: TvRelease) => {
    // The backend decision is the only source of overrides; rejected releases need an extra confirmation.
    const override = !release.decision.allowed
    setGrabbing(release.id)
    setError('')
    setNotice('')
    setQueued(null)
    try {
      await tvApi.grab(seriesId, { ...target, releaseId: release.id, override })
      setConfirmId('')
      setNotice(`Download queued: ${release.title}.`)
      setQueued({ title: release.title, protocol: release.protocol ?? '' })
      onGrabbed?.(seriesId)
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setGrabbing('')
    }
  }

  return (
    <DialogShell
      active={active}
      title="Search releases"
      description={`${seriesTitle || 'Series'} · ${targetLabel(target)}`}
      onClose={onClose}
    >
      <div className="space-y-4">
        <Section
          title={targetLabel(target)}
          action={
            <Button
              size="sm"
              variant={releases === null ? 'default' : 'outline'}
              disabled={searching}
              aria-label={`Search releases for ${seriesTitle || 'series'} ${targetLabel(target)}`}
              onClick={search}
            >
              {searching ? (
                <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
              ) : (
                <SearchIcon data-icon="inline-start" />
              )}
              {searching ? 'Searching…' : releases === null ? 'Search indexers' : 'Search again'}
            </Button>
          }
        >
          {error && <ErrorNote onRetry={search}>{error}</ErrorNote>}
          {searching && releases === null && !error && <LoadingNote>Searching indexers…</LoadingNote>}
          {!searching && releases === null && !error && (
            <EmptyState>
              Nothing is searched until you press Search indexers. Results appear here with size, age, and the
              profile decision, and nothing downloads until you choose a release.
            </EmptyState>
          )}
          {releases !== null && releases.length === 0 && !searching && (
            <EmptyState>No releases matched this target. Try again later or search a different season.</EmptyState>
          )}
          {releases !== null && releases.length > 0 && (
            <ul className="space-y-2">
              {releases.map((release) => {
                const decision = release.decision
                const chips = [
                  decision.details.quality,
                  decision.details.resolution ? `${decision.details.resolution}p` : '',
                  decision.details.source,
                  decision.details.codec,
                  decision.details.audio,
                  decision.details.hdr,
                  decision.details.group,
                  decision.details.edition,
                  decision.details.proper ? 'Proper' : '',
                  decision.details.language,
                ].filter(Boolean)
                const matched = (release.episodeIds ?? []).length
                return (
                  <li key={release.id} className="rounded-lg border border-border p-3">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0">
                        <p className="text-sm font-medium break-words">{release.title}</p>
                        <p className="text-xs text-muted-foreground">
                          <span className="tabular-nums">{formatBytes(release.size)}</span> ·{' '}
                          <time dateTime={release.published} title={new Date(release.published).toLocaleString()}>
                            {formatAge(release.published)}
                          </time>
                          {matched > 1 ? ` · covers ${matched} episodes` : ''}
                          {release.source ? ` · ${release.source}` : ''}
                          {release.protocol === 'torrent' && release.seeders !== undefined ? ` · ${release.seeders} seeders` : ''}
                        </p>
                      </div>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge variant="outline">{release.protocol === 'torrent' ? 'Torrent' : 'Usenet'}</Badge>
                        {release.pack && <Badge variant="outline">Season pack</Badge>}
                        {decision.upgrade && <Badge variant="outline">Upgrade</Badge>}
                        <Badge
                          variant={decision.allowed ? 'outline' : 'destructive'}
                          className={cn(decision.allowed && 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300')}
                        >
                          {decision.allowed ? 'Allowed' : 'Rejected'}
                        </Badge>
                      </div>
                    </div>
                    <div className="mt-2 flex flex-wrap gap-1.5">
                      <Badge variant="outline" className="text-muted-foreground">
                        Score {decision.score}
                      </Badge>
                      {chips.map((chip) => (
                        <Badge key={chip} variant="outline" className="text-muted-foreground">
                          {chip}
                        </Badge>
                      ))}
                    </div>
                    {strings(decision.reasons).length > 0 && (
                      <ul className="mt-2 list-disc space-y-0.5 pl-4 text-xs text-muted-foreground">
                        {strings(decision.reasons).map((reason, index) => (
                          <li key={`${reason}-${index}`}>{reason}</li>
                        ))}
                      </ul>
                    )}
                    {confirmId === release.id ? (
                      <div className="mt-3 flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
                        <CircleAlertIcon className="size-4 shrink-0 text-destructive" />
                        <span className="text-xs text-muted-foreground">
                          This release was rejected. Download it anyway?
                        </span>
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={grabbing === release.id}
                          aria-label={`Confirm downloading rejected release ${release.title}`}
                          onClick={() => void grab(release)}
                        >
                          {grabbing === release.id ? (
                            <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                          ) : (
                            <DownloadIcon data-icon="inline-start" />
                          )}
                          Confirm override
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setConfirmId('')}>
                          Cancel
                        </Button>
                      </div>
                    ) : (
                      <div className="mt-3">
                        <Button
                          size="sm"
                          variant={decision.allowed ? 'default' : 'outline'}
                          disabled={grabbing === release.id}
                          aria-label={
                            decision.allowed
                              ? `Download release ${release.title}`
                              : `Download rejected release ${release.title} anyway`
                          }
                          onClick={() => (decision.allowed ? void grab(release) : setConfirmId(release.id))}
                        >
                          {grabbing === release.id ? (
                            <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                          ) : (
                            <DownloadIcon data-icon="inline-start" />
                          )}
                          {decision.allowed ? 'Download' : 'Download anyway'}
                        </Button>
                      </div>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </Section>
        {notice && (
          <Notice>
            {notice}
            {canReadQueue && (
              <>
                {' '}
                <a
                  href={queueHref(queued?.protocol)}
                  className="underline underline-offset-4"
                  aria-label={`View the queued download in ${queueLabel(queued?.protocol)}`}
                >
                  View download
                </a>
              </>
            )}
          </Notice>
        )}
      </div>
    </DialogShell>
  )
}
