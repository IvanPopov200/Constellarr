import { cn } from 'cn'
import { mediaTypeLabel, type CalendarEntry } from '@/lib/discovery-api'

const weekdayLabels = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']

// mondayOffset keeps the grid aligned to a Monday-first week.
function mondayOffset(date: Date) {
  return (date.getDay() + 6) % 7
}

function dayKey(year: number, month: number, day: number) {
  return `${year}-${String(month + 1).padStart(2, '0')}-${String(day).padStart(2, '0')}`
}

export function CalendarMonth({ month, entries, selectedDay, onSelectDay, today }: {
  month: Date
  entries: CalendarEntry[]
  selectedDay: string
  onSelectDay: (day: string) => void
  today: string
}) {
  const year = month.getFullYear()
  const monthIndex = month.getMonth()
  const first = new Date(year, monthIndex, 1)
  const daysInMonth = new Date(year, monthIndex + 1, 0).getDate()
  const offset = mondayOffset(first)
  const cells: (number | null)[] = []
  for (let i = 0; i < offset; i++) cells.push(null)
  for (let day = 1; day <= daysInMonth; day++) cells.push(day)

  const byDay = new Map<string, CalendarEntry[]>()
  for (const entry of entries) {
    const key = entry.date.slice(0, 10)
    const list = byDay.get(key)
    if (list) list.push(entry)
    else byDay.set(key, [entry])
  }

  return (
    <div className="rounded-xl border border-border">
      <div className="grid grid-cols-7 border-b border-border text-center text-xs text-muted-foreground">
        {weekdayLabels.map((label) => (
          <div key={label} className="px-2 py-2 font-medium">
            {label}
          </div>
        ))}
      </div>
      <div className="grid grid-cols-7">
        {cells.map((day, index) => {
          if (day === null) return <div key={`empty-${index}`} className="min-h-16 border-r border-b border-border/50 last:border-r-0 sm:min-h-24" />
          const key = dayKey(year, monthIndex, day)
          const dayEntries = byDay.get(key) ?? []
          const selected = key === selectedDay
          return (
            <button
              key={key}
              type="button"
              onClick={() => onSelectDay(key)}
              aria-label={`${new Date(year, monthIndex, day).toLocaleDateString(undefined, { dateStyle: 'full' })}, ${dayEntries.length} releases`}
              aria-pressed={selected}
              className={cn(
                'min-h-16 space-y-1 border-r border-b border-border/50 p-1.5 text-left align-top transition-colors last:border-r-0 hover:bg-muted/40 sm:min-h-24',
                selected && 'bg-primary/10 ring-1 ring-primary/40 ring-inset',
              )}
            >
              <span className={cn('flex items-center justify-between text-xs', key === today ? 'font-semibold text-primary' : 'text-muted-foreground')}>
                {day}
                {dayEntries.length > 0 && <span className="tabular-nums">{dayEntries.length}</span>}
              </span>
              {dayEntries.slice(0, 2).map((entry) => (
                <span key={entry.id} className="flex items-center gap-1 text-[0.7rem] leading-tight">
                  <span
                    className={cn(
                      'size-1.5 shrink-0 rounded-full',
                      entry.mediaType === 'movie' ? 'bg-sky-400' : entry.mediaType === 'tv' ? 'bg-violet-400' : 'bg-emerald-400',
                    )}
                    aria-hidden="true"
                  />
                  <span className="truncate">{entry.title}</span>
                  <span className="sr-only">{mediaTypeLabel(entry.mediaType)}</span>
                </span>
              ))}
              {dayEntries.length > 2 && <span className="block text-[0.7rem] text-muted-foreground">+{dayEntries.length - 2} more</span>}
            </button>
          )
        })}
      </div>
    </div>
  )
}
