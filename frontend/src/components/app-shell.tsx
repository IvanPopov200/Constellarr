import { useEffect, useId, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Dialog } from 'radix-ui'
import {
  CableIcon,
  CalendarDaysIcon,
  CaptionsIcon,
  ChevronRightIcon,
  DownloadIcon,
  FilmIcon,
  HardDriveIcon,
  InboxIcon,
  LayoutDashboardIcon,
  LogOutIcon,
  MagnetIcon,
  MenuIcon,
  MusicIcon,
  ImportIcon,
  PanelLeftCloseIcon,
  PanelLeftIcon,
  SettingsIcon,
  ShieldCheckIcon,
  TvIcon,
  UsersIcon,
  XIcon,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { BackendStatusChip } from '@/components/backend-status'
import { Button } from '@/components/ui/button'
import { cn } from 'cn'
import { routeLabels, routePermissions, type Route } from '@/lib/navigation'
import { useAuth } from '@/lib/auth-context'

const routeSections: Record<Route, string> = {
  overview: 'Workspace',
  requests: 'Workspace',
  movies: 'Workspace',
  'tv-shows': 'Workspace',
  music: 'Workspace',
  subtitles: 'Workspace',
  calendar: 'Workspace',
  usenet: 'Downloads',
  torrents: 'Downloads',
  connections: 'System',
  storage: 'System',
  users: 'System',
  migration: 'System',
  backups: 'System',
  system: 'System',
}

const navSections: { label: string; items: { route: Route; icon: LucideIcon }[] }[] = [
  {
    label: 'Workspace',
    items: [
      { route: 'overview', icon: LayoutDashboardIcon },
      { route: 'requests', icon: InboxIcon },
      { route: 'movies', icon: FilmIcon },
      { route: 'tv-shows', icon: TvIcon },
      { route: 'music', icon: MusicIcon },
      { route: 'subtitles', icon: CaptionsIcon },
      { route: 'calendar', icon: CalendarDaysIcon },
    ],
  },
  {
    label: 'Downloads',
    items: [
      { route: 'usenet', icon: DownloadIcon },
      { route: 'torrents', icon: MagnetIcon },
    ],
  },
  {
    label: 'System',
    items: [
      { route: 'connections', icon: CableIcon },
      { route: 'storage', icon: HardDriveIcon },
      { route: 'users', icon: UsersIcon },
      { route: 'migration', icon: ImportIcon },
      { route: 'backups', icon: ShieldCheckIcon },
      { route: 'system', icon: SettingsIcon },
    ],
  },
]

const collapsedStorageKey = 'constellarr:sidebar-collapsed'

function readCollapsed() {
  try {
    return window.localStorage.getItem(collapsedStorageKey) === '1'
  } catch {
    return false
  }
}

function ConstellationMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" className={className}>
      <path
        d="M5 18 9 9l7 3 3-7"
        fill="none"
        stroke="currentColor"
        strokeLinecap="round"
        strokeWidth="1.5"
      />
      <circle cx="5" cy="18" r="1.7" fill="currentColor" />
      <circle cx="9" cy="9" r="1.7" fill="currentColor" />
      <circle cx="16" cy="12" r="1.7" fill="currentColor" />
      <circle cx="19" cy="5" r="1.7" fill="currentColor" />
    </svg>
  )
}

function Brand({ collapsed = false }: { collapsed?: boolean }) {
  return (
    <div className={cn('flex h-14 items-center gap-2.5 px-4', collapsed && 'justify-center px-0')}>
      <ConstellationMark className="size-6 shrink-0 text-primary" />
      {!collapsed && (
        <span className="font-heading text-sm font-semibold tracking-tight">Constellarr</span>
      )}
    </div>
  )
}

function NavLink({
  route,
  label,
  icon: Icon,
  active,
  collapsed,
  badge,
  onNavigate,
}: {
  route: Route
  label: string
  icon: LucideIcon
  active: boolean
  collapsed: boolean
  badge: number
  onNavigate?: () => void
}) {
  return (
    <a
      href={`#${route}`}
      aria-current={active ? 'page' : undefined}
      title={collapsed ? label : undefined}
      onClick={onNavigate}
      className={cn(
        'relative flex h-8 items-center gap-2.5 rounded-md px-3 text-sm font-medium text-sidebar-foreground/80 transition-colors',
        'hover:bg-sidebar-accent hover:text-sidebar-accent-foreground',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring',
        collapsed && 'justify-center px-0',
        active && 'bg-sidebar-primary/10 text-sidebar-primary hover:bg-sidebar-primary/15 hover:text-sidebar-primary',
      )}
    >
      <Icon className="size-4 shrink-0" aria-hidden="true" />
      <span className={cn('truncate', collapsed && 'sr-only')}>{label}</span>
      {badge > 0 &&
        (collapsed ? (
          <span
            aria-hidden="true"
            className="absolute top-1.5 right-2.5 size-1.5 rounded-full bg-sidebar-primary"
          />
        ) : (
          <span
            aria-hidden="true"
            className="ml-auto rounded-full bg-sidebar-primary/15 px-1.5 text-[11px] font-semibold tabular-nums text-sidebar-primary"
          >
            {badge}
          </span>
        ))}
      {badge > 0 && <span className="sr-only">{`, ${badge} active`}</span>}
    </a>
  )
}

function NavList({
  route,
  activeDownloads,
  collapsed,
  onNavigate,
}: {
  route: Route
  activeDownloads: number
  collapsed: boolean
  onNavigate?: () => void
}) {
  const id = useId()
  const { can } = useAuth()

  return (
    <nav aria-label="Primary" className="flex flex-1 flex-col gap-6 overflow-y-auto px-3 py-4">
      {navSections.map((section) => {
        const items = section.items.filter(item => !routePermissions[item.route] || can(routePermissions[item.route]!))
        if (items.length === 0) return null
        const headingId = `${id}-${section.label}`
        return (
          <div key={section.label}>
            <p
              id={headingId}
              className={cn(
                'px-3 pb-1.5 text-[11px] font-medium tracking-[0.12em] text-muted-foreground uppercase',
                collapsed && 'sr-only',
              )}
            >
              {section.label}
            </p>
            <ul aria-labelledby={headingId} className="flex flex-col gap-0.5">
              {items.map((item) => (
                <li key={item.route}>
                  <NavLink
                    route={item.route}
                    label={routeLabels[item.route]}
                    icon={item.icon}
                    active={route === item.route}
                    collapsed={collapsed}
                    badge={item.route === 'usenet' ? activeDownloads : 0}
                    onNavigate={onNavigate}
                  />
                </li>
              ))}
            </ul>
          </div>
        )
      })}
    </nav>
  )
}

export function PageHeading({
  title,
  description,
  action,
}: {
  title: string
  description?: string
  action?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {action}
    </div>
  )
}

export function AppShell({
  route,
  activeDownloads,
  children,
}: {
  route: Route
  activeDownloads: number
  children: ReactNode
}) {
  const [mobileOpen, setMobileOpen] = useState(false)
  const { can, user, logout } = useAuth()
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const mainRef = useRef<HTMLElement>(null)
  const navigatedRef = useRef(false)
  const firstRoute = useRef(true)

  useEffect(() => {
    try {
      window.localStorage.setItem(collapsedStorageKey, collapsed ? '1' : '0')
    } catch {
      // Storage can be unavailable in private modes; collapsing still works for the session.
    }
  }, [collapsed])

  useEffect(() => {
    document.title = `${routeLabels[route]} · Constellarr`
  }, [route])

  useEffect(() => {
    if (firstRoute.current) {
      firstRoute.current = false
      return
    }
    mainRef.current?.focus({ preventScroll: true })
    window.scrollTo({ top: 0 })
  }, [route])

  const navigateFromDialog = () => {
    navigatedRef.current = true
    setMobileOpen(false)
  }

  return (
    <div className="min-h-svh">
      <aside
        className={cn(
          'fixed inset-y-0 left-0 z-40 hidden flex-col border-r border-sidebar-border bg-sidebar lg:flex',
          collapsed ? 'w-16' : 'w-60',
        )}
      >
        <div className="border-b border-sidebar-border">
          <Brand collapsed={collapsed} />
        </div>
        <NavList
          route={route}
          activeDownloads={activeDownloads}
          collapsed={collapsed}
        />
      </aside>

      <div className={cn('flex min-h-svh flex-col', collapsed ? 'lg:pl-16' : 'lg:pl-60')}>
        <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-1.5 border-b border-border bg-background/95 px-3 backdrop-blur sm:px-5">
          <Dialog.Root open={mobileOpen} onOpenChange={setMobileOpen}>
            <Dialog.Trigger asChild>
              <Button variant="ghost" size="icon-sm" className="lg:hidden" aria-label="Open navigation">
                <MenuIcon />
              </Button>
            </Dialog.Trigger>
            <Dialog.Portal>
              <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 motion-reduce:animate-none" />
              <Dialog.Content
                onCloseAutoFocus={(event) => {
                  if (!navigatedRef.current) return
                  navigatedRef.current = false
                  event.preventDefault()
                  mainRef.current?.focus()
                }}
                className="fixed inset-y-0 left-0 z-50 flex w-72 max-w-[85vw] flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground shadow-xl data-[state=open]:animate-in data-[state=open]:slide-in-from-left data-[state=closed]:animate-out data-[state=closed]:slide-out-to-left motion-reduce:animate-none"
              >
                <Dialog.Title className="sr-only">Navigation</Dialog.Title>
                <Dialog.Description className="sr-only">
                  Constellarr sections: media and requests, downloads, and system settings.
                </Dialog.Description>
                <div className="flex h-14 shrink-0 items-center justify-between border-b border-sidebar-border pr-2">
                  <Brand />
                  <Dialog.Close asChild>
                    <Button variant="ghost" size="icon-sm" aria-label="Close navigation">
                      <XIcon />
                    </Button>
                  </Dialog.Close>
                </div>
                <NavList
                  route={route}
                  activeDownloads={activeDownloads}
                  collapsed={false}
                  onNavigate={navigateFromDialog}
                />
              </Dialog.Content>
            </Dialog.Portal>
          </Dialog.Root>

          <Button
            variant="ghost"
            size="icon-sm"
            className="hidden lg:inline-flex"
            aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
            onClick={() => setCollapsed((current) => !current)}
          >
            {collapsed ? <PanelLeftIcon /> : <PanelLeftCloseIcon />}
          </Button>

          <nav aria-label="Breadcrumb" className="min-w-0">
            <ol className="flex items-center gap-1.5 text-sm">
              <li className="hidden text-muted-foreground sm:block">{routeSections[route]}</li>
              <li aria-hidden="true" className="hidden sm:block">
                <ChevronRightIcon className="size-3.5 text-muted-foreground/70" />
              </li>
              <li aria-current="page" className="truncate font-medium">
                {routeLabels[route]}
              </li>
            </ol>
          </nav>

          <div className="ml-auto flex items-center gap-2">
            {can('monitoring.read') && <BackendStatusChip />}
            <Button asChild variant="ghost" size="sm"><a href="#users" aria-label="My account"><UsersIcon /><span className="hidden max-w-32 truncate sm:inline">{user?.name}</span></a></Button>
            <Button variant="ghost" size="icon-sm" aria-label="Sign out" onClick={() => void logout().catch(() => {})}><LogOutIcon /></Button>
          </div>
        </header>

        <main
          id="main-content"
          ref={mainRef}
          tabIndex={-1}
          className="flex-1 px-4 py-6 outline-none sm:px-8 sm:py-8 lg:px-10 xl:px-12"
        >
          <div className="mx-auto w-full max-w-7xl">{children}</div>
        </main>
      </div>
    </div>
  )
}
