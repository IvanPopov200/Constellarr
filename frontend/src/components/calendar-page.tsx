import { useCallback, useEffect, useMemo, useState } from 'react'
import { CalendarDays, ChevronLeft, ChevronRight, Download, ExternalLink, LoaderCircle, RefreshCw } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { EmptyNote, ErrorNote, LoadingNote, NoteBanner, Choice } from '@/components/requests-shared'
import { CalendarMonth } from '@/components/calendar-month'
import { errorMessage } from '@/lib/api'
import {
  catalogHref,
  discoveryApi,
  formatDay,
  isoDay,
  mediaTypeLabel,
  type CalendarEntry,
  type CalendarView,
  type MediaType,
} from '@/lib/discovery-api'
import { cn } from 'cn'

type ViewMode = 'month' | 'list' | 'upcoming'

const allTypes: MediaType[] = ['movie', 'tv', 'music']

export function CalendarPage() {
  const today = useMemo(() => isoDay(new Date()), [])
  const [view, setView] = useState<ViewMode>('upcoming')
  const [types, setTypes] = useState<MediaType[]>(['movie', 'tv'])
  const [from, setFrom] = useState(today)
  const [to, setTo] = useState(() => {
    const end = new Date()
    end.setDate(end.getDate() + 90)
    return isoDay(end)
  })
  const [month, setMonth] = useState(() => {
    const now = new Date()
    return new Date(now.getFullYear(), now.getMonth(), 1)
  })
  const [data, setData] = useState<CalendarView | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selectedDay, setSelectedDay] = useState(today)

  const monthRange = useMemo(() => {
    const start = new Date(month.getFullYear(), month.getMonth(), 1)
    const end = new Date(month.getFullYear(), month.getMonth() + 1, 0)
    return { from: isoDay(start), to: isoDay(end) }
  }, [month])

  const query = useMemo(() => {
    const range = view === 'month' ? monthRange : { from, to }
    return { ...range, types }
  }, [view, monthRange, from, to, types])

  const reload = useCallback(async () => {
    try {
      const loaded = await discoveryApi.calendar(query)
      setData(loaded)
      setError('')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setLoading(false)
    }
  }, [query])

  useEffect(() => {
    const controller = new AbortController()
    discoveryApi
      .calendar(query, controller.signal)
      .then((loaded) => {
        setData(loaded)
        setError('')
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [query])

  const sources = useMemo(() => data?.sources ?? [], [data])
  const entries = useMemo(() => data?.entries ?? [], [data])
  const selectedEntries = entries.filter((entry) => entry.date.slice(0, 10) === selectedDay)

  const grouped = useMemo(() => {
    if (view === 'month') return []
    const groups: { date: string; entries: CalendarEntry[] }[] = []
    for (const entry of entries) {
      const date = entry.date.slice(0, 10)
      const last = groups[groups.length - 1]
      if (last && last.date === date) last.entries.push(entry)
      else groups.push({ date, entries: [entry] })
    }
    return view === 'upcoming' ? groups.slice(0, 20) : groups
  }, [entries, view])

  function toggleType(type: MediaType) {
    setTypes((current) => (current.includes(type) ? current.filter((value) => value !== type) : [...current, type]))
  }

  function selectMonth(value: Date) {
    setMonth(value)
    setSelectedDay(isoDay(value))
  }

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div className="space-y-1">
          <h1 className="text-2xl font-semibold tracking-tight">Calendar</h1>
          <p className="text-sm text-muted-foreground">
            Upcoming movie releases, TV episodes, and album releases from every configured source.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => void reload()} disabled={loading}>
            {loading ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}
            Refresh
          </Button>
          <Button asChild size="sm">
            <a href={discoveryApi.calendarIcsUrl(query)}>
              <Download data-icon="inline-start" />
              Subscribe (.ics)
            </a>
          </Button>
        </div>
      </header>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap gap-1.5" role="tablist" aria-label="Calendar view">
          {(['month', 'list', 'upcoming'] as ViewMode[]).map((mode) => (
            <Button
              key={mode}
              size="sm"
              role="tab"
              aria-selected={view === mode}
              variant={view === mode ? 'secondary' : 'ghost'}
              onClick={() => setView(mode)}
            >
              {mode === 'month' ? 'Month' : mode === 'list' ? 'List' : 'Upcoming'}
            </Button>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {allTypes.map((type) => {
            const source = sources.find((item) => item.mediaType === type)
            const available = source ? source.available : type !== 'music'
            return (
              <Button
                key={type}
                size="sm"
                variant={types.includes(type) ? 'outline' : 'ghost'}
                aria-pressed={types.includes(type)}
                disabled={!available}
                title={available ? undefined : `${mediaTypeLabel(type)} is not configured on this server.`}
                onClick={() => toggleType(type)}
              >
                {mediaTypeLabel(type)}
                {!available && <Badge variant="ghost" className="text-muted-foreground">unavailable</Badge>}
              </Button>
            )
          })}
        </div>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        {view === 'month' ? (
          <div className="flex items-center gap-2">
            <Button variant="outline" size="icon-sm" aria-label="Previous month" onClick={() => selectMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}>
              <ChevronLeft />
            </Button>
            <span className="min-w-40 text-center text-sm font-medium">
              {month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}
            </span>
            <Button variant="outline" size="icon-sm" aria-label="Next month" onClick={() => selectMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}>
              <ChevronRight />
            </Button>
            <Button variant="ghost" size="sm" onClick={() => selectMonth(new Date())}>
              Today
            </Button>
          </div>
        ) : (
          <>
            <label className="space-y-1 text-xs text-muted-foreground">
              <span className="block font-medium">From</span>
              <Input type="date" value={from} max={to} onChange={(event) => setFrom(event.target.value)} />
            </label>
            <label className="space-y-1 text-xs text-muted-foreground">
              <span className="block font-medium">To</span>
              <Input type="date" value={to} min={from} onChange={(event) => setTo(event.target.value)} />
            </label>
            <Choice
              aria-label="Range preset"
              onChange={(event) => {
                const days = Number(event.target.value)
                const end = new Date()
                end.setDate(end.getDate() + days)
                setFrom(today)
                setTo(isoDay(end))
              }}
              defaultValue="90"
            >
              <option value="30">Next 30 days</option>
              <option value="90">Next 90 days</option>
              <option value="180">Next 180 days</option>
              <option value="365">Next year</option>
            </Choice>
          </>
        )}
        {data && (
          <p className="text-xs text-muted-foreground">
            {data.entries?.length ?? 0} entries · {formatDay(data.from)} → {formatDay(data.to)}
          </p>
        )}
      </div>

      {error && <ErrorNote onRetry={() => void reload()}>{error}</ErrorNote>}
      {sources.some((source) => !source.available) && (
        <NoteBanner tone="info">
          {sources
            .filter((source) => !source.available)
            .map((source) => `${mediaTypeLabel(source.mediaType)} is not configured on this server, so its calendar is empty.`)
            .join(' ')}
        </NoteBanner>
      )}

      {loading && entries.length === 0 && <LoadingNote>Loading the calendar…</LoadingNote>}
      {!loading && view !== 'month' && entries.length === 0 && <EmptyNote>No releases in this range. Widen the dates or enable another media type.</EmptyNote>}

      {view === 'month' && !loading && (
        <div className="space-y-4">
          <CalendarMonth month={month} entries={entries} selectedDay={selectedDay} onSelectDay={setSelectedDay} today={today} />
          <div className="space-y-2">
            <h2 className="font-heading text-sm font-semibold">{formatDay(selectedDay)}</h2>
            {selectedEntries.length === 0 ? (
              <EmptyNote>Nothing scheduled for this day.</EmptyNote>
            ) : (
              <ul className="space-y-2">
                {selectedEntries.map((entry) => (
                  <EntryRow key={entry.id} entry={entry} />
                ))}
              </ul>
            )}
          </div>
        </div>
      )}

      {view !== 'month' && grouped.length > 0 && (
        <div className="space-y-5">
          {grouped.map((group) => (
            <section key={group.date} className="space-y-2">
              <h2 className="flex items-center gap-2 font-heading text-sm font-semibold">
                <CalendarDays className="size-4 text-muted-foreground" aria-hidden="true" />
                {formatDay(group.date)}
                <Badge variant="ghost" className="text-muted-foreground">{group.entries.length}</Badge>
              </h2>
              <ul className="space-y-2">
                {group.entries.map((entry) => (
                  <EntryRow key={entry.id} entry={entry} />
                ))}
              </ul>
            </section>
          ))}
          {view === 'upcoming' && grouped.length < entries.length && (
            <p className="text-xs text-muted-foreground">Showing the next 20 release days. Use List or a date range for more.</p>
          )}
        </div>
      )}
    </div>
  )
}

function EntryRow({ entry }: { entry: CalendarEntry }) {
  const href = catalogHref(entry.mediaType, entry.libraryId)
  return (
    <li>
      <Card className="shadow-none">
        <CardContent className="flex-row items-center gap-3">
          {entry.poster ? (
            <img src={entry.poster} alt="" loading="lazy" className="h-14 w-10 shrink-0 rounded object-cover" />
          ) : (
            <span
              className={cn(
                'h-14 w-10 shrink-0 rounded',
                entry.mediaType === 'movie' ? 'bg-sky-500/20' : entry.mediaType === 'tv' ? 'bg-violet-500/20' : 'bg-emerald-500/20',
              )}
              aria-hidden="true"
            />
          )}
          <div className="min-w-0 flex-1">
            <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
              {entry.title}
              {entry.year > 0 && <span className="text-xs text-muted-foreground">{entry.year}</span>}
              <Badge variant="ghost" className="text-muted-foreground">{mediaTypeLabel(entry.mediaType)}</Badge>
            </p>
            {entry.subtitle && <p className="truncate text-xs text-muted-foreground">{entry.subtitle}</p>}
            <p className="text-xs text-muted-foreground">
              {entry.artist && <span>{entry.artist} · </span>}
              {formatDay(entry.date)}
              {entry.status ? ` · ${entry.status}` : ''}
            </p>
          </div>
          {href && (
            <Button asChild size="sm" variant="ghost">
              <a href={href}>
                <ExternalLink data-icon="inline-start" />
                Library
              </a>
            </Button>
          )}
        </CardContent>
      </Card>
    </li>
  )
}
