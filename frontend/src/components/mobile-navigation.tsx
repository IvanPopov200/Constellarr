import { CalendarDaysIcon, DownloadIcon, FilmIcon, LayoutDashboardIcon, MenuIcon } from 'lucide-react'
import { cn } from 'cn'
import { useAuth } from '@/lib/auth-context'
import type { Route } from '@/lib/navigation'

const destinations = [
  { route: 'overview', label: 'Home', icon: LayoutDashboardIcon, permission: null, related: ['overview', 'requests'] },
  { route: 'movies', label: 'Library', icon: FilmIcon, permission: 'library.read', related: ['movies', 'tv-shows', 'music', 'subtitles'] },
  { route: 'usenet', label: 'Activity', icon: DownloadIcon, permission: 'downloads.read', related: ['usenet', 'torrents'] },
  { route: 'calendar', label: 'Calendar', icon: CalendarDaysIcon, permission: 'library.read', related: ['calendar'] },
] as const

export function MobileNavigation({ route, activeDownloads, onMore }: {
  route: Route
  activeDownloads: number
  onMore: (trigger: HTMLButtonElement) => void
}) {
  const { can } = useAuth()
  const visible = destinations.filter(item => !item.permission || can(item.permission))
  const inSystem = !destinations.some(item => (item.related as readonly string[]).includes(route))
  const itemClass = 'relative flex min-h-14 min-w-0 flex-1 flex-col items-center justify-center gap-1 rounded-xl px-1 text-[11px] font-bold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

  return (
    <nav aria-label="Quick navigation" className="fixed inset-x-4 bottom-[max(12px,env(safe-area-inset-bottom))] z-40 mx-auto flex max-w-lg rounded-3xl border border-border bg-sidebar/95 p-2 shadow-xl backdrop-blur-xl lg:hidden">
      {visible.map(({ route: target, label, icon: Icon, related }) => {
        const selected = (related as readonly string[]).includes(route)
        return (
          <a key={target} href={`#${target}`} aria-current={selected ? 'page' : undefined}
            className={cn(itemClass, selected ? 'bg-primary/10 text-primary' : 'text-muted-foreground hover:bg-accent hover:text-foreground')}>
            <Icon className="size-5" aria-hidden="true" />
            <span>{label}</span>
            {target === 'usenet' && activeDownloads > 0 && <>
              <span aria-hidden="true" className="absolute top-1 right-2 rounded-full bg-primary px-1 text-[10px] text-primary-foreground">{activeDownloads > 99 ? '99+' : activeDownloads}</span>
              <span className="sr-only">{activeDownloads} active downloads</span>
            </>}
          </a>
        )
      })}
      <button type="button" onClick={event => onMore(event.currentTarget)} aria-label="More sections" aria-haspopup="dialog"
        className={cn(itemClass, inSystem ? 'bg-primary/10 text-primary' : 'text-muted-foreground hover:bg-accent hover:text-foreground')}>
        <MenuIcon className="size-5" aria-hidden="true" />
        <span>More</span>
      </button>
    </nav>
  )
}
