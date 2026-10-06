import { useEffect, useSyncExternalStore } from 'react'
import type { MouseEvent } from 'react'
import { AppShell, PageHeading } from '@/components/app-shell'
import { routeLabels, routePermissions, type Route } from '@/lib/navigation'
import { useAuth } from '@/lib/auth-context'
import { UsersPage } from '@/components/users-page'
import { RequestsPage } from '@/components/requests-page'
import { CalendarPage } from '@/components/calendar-page'
import { RecommendationsPanel } from '@/components/recommendations-panel'
import { AISettings } from '@/components/recommendations-ai-settings'
import { MigrationPage } from '@/components/migration-page'
import { DownloadQueue } from '@/components/download-queue'
import { DownloadControls } from '@/components/download-controls'
import { Overview } from '@/components/overview'
import { ReleaseSearch } from '@/components/release-search'
import { MoviesPage } from '@/components/movies-page'
import { TVPage } from '@/components/tv-page'
import { MusicPage, MusicSettings } from '@/components/music-page'
import { SubtitlesPage } from '@/components/subtitles-page'
import { TorrentsPage } from '@/components/torrents-page'
import { OperationsPage } from '@/components/operations-page'
import { BackupPanel } from '@/components/operations-backup-panel'
import { SettingsPage } from '@/components/settings-page'
import { Button } from '@/components/ui/button'
import { isActiveJob } from '@/lib/api'
import { useDownloads } from '@/lib/use-downloads'

// Dark is the only theme; mark the document so dark: utilities apply before first paint.
document.documentElement.classList.add('dark')

const routeIds = Object.keys(routeLabels) as Route[]
const routeAliases: Record<string, Route> = { search: 'movies', downloads: 'usenet', settings: 'connections' }

function readHash() {
  return window.location.hash
}

function readRoute(hash: string): Route {
  const path = hash.replace(/^#\/?/, '').split('?')[0]
  const value = routeAliases[path] ?? path
  return routeIds.includes(value as Route) ? (value as Route) : 'overview'
}

function subscribeToHash(onStoreChange: () => void) {
  window.addEventListener('hashchange', onStoreChange)
  return () => window.removeEventListener('hashchange', onStoreChange)
}

function skipToContent(event: MouseEvent<HTMLAnchorElement>) {
  event.preventDefault()
  document.getElementById('main-content')?.focus()
}

function App() {
  const { can } = useAuth()
  const { jobs, error, refresh, download, retry, pause, resume, cancel } = useDownloads(can('downloads.read'))
  const hash = useSyncExternalStore(subscribeToHash, readHash, () => '#overview')
  const requestedRoute = readRoute(hash)
  const permission = routePermissions[requestedRoute]
  const route = permission && !can(permission) ? 'overview' : requestedRoute

  useEffect(() => {
    if (hash.split('?')[0] !== `#${route}`) {
      window.history.replaceState(null, '', `#${route}`)
    }
  }, [hash, route])

  const activeDownloads = jobs?.filter(isActiveJob).length ?? 0
  const settingsSection = route === 'connections' || route === 'storage' ? route : null
  const subtitleParams = new URLSearchParams(hash.split('?')[1])
  const subtitleKind = subtitleParams.get('kind')
  const subtitleId = subtitleParams.get('id')
  const subtitleTarget = (subtitleKind === 'movie' || subtitleKind === 'episode') && subtitleId
    ? { kind: subtitleKind, id: subtitleId, mode: 'search' as const }
    : undefined

  return (
    <>
      <a
        href="#main-content"
        onClick={skipToContent}
        className="sr-only focus:not-sr-only focus:fixed focus:top-4 focus:left-4 focus:z-[60] focus:rounded-md focus:bg-primary focus:px-3 focus:py-2 focus:text-sm focus:font-medium focus:text-primary-foreground"
      >
        Skip to content
      </a>
      <AppShell route={route} activeDownloads={activeDownloads}>
        {route === 'overview' && (
          <div className="space-y-6">
            <Overview jobs={jobs} error={error} onRetry={retry} onPause={pause} onResume={resume} onCancel={cancel} onRefresh={refresh} />
            {can('library.read') && <RecommendationsPanel />}
          </div>
        )}

        {can('library.read') && <div hidden={route !== 'movies'} inert={route !== 'movies'}>
          <div className="flex flex-col gap-6">
            <MoviesPage />
            {can('downloads.write') && <details className="rounded-xl border border-border p-4">
              <summary className="cursor-pointer text-sm font-medium">Search an NZB release directly</summary>
              <div className="mt-4"><ReleaseSearch jobs={jobs ?? []} onDownload={download} /></div>
            </details>}
          </div>
        </div>}

        {can('library.read') && <div hidden={route !== 'tv-shows'} inert={route !== 'tv-shows'}>
          <TVPage active={route === 'tv-shows'} />
        </div>}

        {route === 'users' && <UsersPage />}
        {route === 'requests' && <RequestsPage />}
        {route === 'calendar' && <CalendarPage />}
        {route === 'migration' && <MigrationPage />}
        {route === 'music' && <MusicPage />}
        {route === 'subtitles' && <SubtitlesPage key={hash} target={subtitleTarget} />}
        {route === 'torrents' && <TorrentsPage />}
        {route === 'system' && <OperationsPage />}
        {route === 'backups' && <div className="space-y-6">
          <PageHeading title="Backups" description="Back up, import, and restore your server configuration and library database." />
          <BackupPanel />
        </div>}

        {route === 'usenet' && (
          <div className="flex flex-col gap-6">
            <PageHeading
              title="Usenet"
              description="Manage downloads and choose when they use your bandwidth."
              action={
                can('library.read') ? <Button asChild size="sm">
                  <a href="#movies">Search releases</a>
                </Button> : undefined
              }
            />
            <DownloadControls />
            <DownloadQueue
              jobs={jobs}
              error={error}
              onRetry={retry}
              onPause={pause}
              onResume={resume}
              onCancel={cancel}
              onRefresh={refresh}
              emptyAction={can('library.read') ? { label: 'Search releases', href: '#movies' } : undefined}
            />
          </div>
        )}

        {settingsSection && can('settings.read') && <div>
          <SettingsPage section={settingsSection} />
          <div className="mt-6"><MusicSettings section={settingsSection} /></div>
          {settingsSection === 'connections' && <div className="mt-6"><AISettings /></div>}
        </div>}
      </AppShell>
    </>
  )
}

export default App
