import { useEffect, useSyncExternalStore } from 'react'
import type { MouseEvent } from 'react'
import { AppShell, PageHeading } from '@/components/app-shell'
import { routeLabels, type Route } from '@/lib/navigation'
import { DownloadQueue } from '@/components/download-queue'
import { Overview } from '@/components/overview'
import { ReleaseSearch } from '@/components/release-search'
import { MoviesPage } from '@/components/movies-page'
import { TVPage } from '@/components/tv-page'
import { SettingsPage } from '@/components/settings-page'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { isActiveJob } from '@/lib/api'
import { useDownloads } from '@/lib/use-downloads'

// Dark is the only theme; mark the document so dark: utilities apply before first paint.
document.documentElement.classList.add('dark')

const routeIds = Object.keys(routeLabels) as Route[]
const routeAliases: Record<string, Route> = { search: 'movies', downloads: 'usenet', settings: 'connections' }
const plannedSections: Partial<Record<Route, string>> = {
  requests: 'Track requests for movies, shows, and music.',
  music: 'Manage artists, albums, and your music library.',
  subtitles: 'Find and manage subtitles for your movies and shows.',
  torrents: 'Manage torrent downloads and seeding.',
  users: 'Manage accounts and access to your server.',
}

function readRoute(): Route {
  const hash = window.location.hash.replace(/^#\/?/, '')
  const value = routeAliases[hash] ?? hash
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
  const { jobs, error, refresh, download, retry } = useDownloads()
  const route = useSyncExternalStore(subscribeToHash, readRoute, () => 'overview' as Route)

  useEffect(() => {
    if (window.location.hash !== `#${route}`) {
      window.history.replaceState(null, '', `#${route}`)
    }
  }, [route])

  const plannedDescription = plannedSections[route]
  const activeDownloads = jobs?.filter(isActiveJob).length ?? 0
  const settingsSection = route === 'connections' || route === 'storage' || route === 'system' ? route : null

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
          <Overview jobs={jobs} error={error} onRetry={retry} onRefresh={refresh} />
        )}

        <div hidden={route !== 'movies'} inert={route !== 'movies'}>
          <div className="flex flex-col gap-6">
            <MoviesPage />
            <details className="rounded-xl border border-border p-4">
              <summary className="cursor-pointer text-sm font-medium">Search an NZB release directly</summary>
              <div className="mt-4"><ReleaseSearch jobs={jobs ?? []} onDownload={download} /></div>
            </details>
          </div>
        </div>

        <div hidden={route !== 'tv-shows'} inert={route !== 'tv-shows'}>
          <TVPage active={route === 'tv-shows'} />
        </div>

        {route === 'usenet' && (
          <div className="flex flex-col gap-6">
            <PageHeading
              title="Usenet"
              description="Transfer and processing stages for queued NZB releases."
              action={
                <Button asChild size="sm">
                  <a href="#movies">Search releases</a>
                </Button>
              }
            />
            <DownloadQueue
              jobs={jobs}
              error={error}
              onRetry={retry}
              onRefresh={refresh}
              emptyAction={{ label: 'Search releases', href: '#movies' }}
            />
          </div>
        )}

        {plannedDescription && (
          <div className="flex flex-col gap-6">
            <PageHeading title={routeLabels[route]} description={plannedDescription} />
            <Card className="max-w-3xl">
              <CardContent className="items-start gap-4">
                <p className="text-sm text-muted-foreground">This section is planned and isn’t available yet.</p>
                {route === 'requests' && <Button asChild size="sm"><a href="#movies">Find a movie</a></Button>}
              </CardContent>
            </Card>
          </div>
        )}

        <div hidden={!settingsSection} inert={!settingsSection}>
          <SettingsPage section={settingsSection ?? 'connections'} />
        </div>
      </AppShell>
    </>
  )
}

export default App
