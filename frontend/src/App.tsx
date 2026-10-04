import { useEffect, useSyncExternalStore } from 'react'
import type { MouseEvent } from 'react'
import { AppShell, PageHeading, type Route } from '@/components/app-shell'
import { DownloadQueue } from '@/components/download-queue'
import { Overview } from '@/components/overview'
import { ReleaseSearch } from '@/components/release-search'
import { SettingsPage } from '@/components/settings-page'
import { Button } from '@/components/ui/button'
import { isActiveJob } from '@/lib/api'
import { useDownloads } from '@/lib/use-downloads'

// Dark is the only theme; mark the document so dark: utilities apply before first paint.
document.documentElement.classList.add('dark')

const routeIds: readonly Route[] = ['overview', 'search', 'downloads', 'settings']

function readRoute(): Route {
  const value = window.location.hash.replace(/^#\/?/, '')
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

  const activeDownloads = jobs?.filter(isActiveJob).length ?? 0

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

        <div hidden={route !== 'search'} inert={route !== 'search'}>
          <div className="flex flex-col gap-6">
            <PageHeading
              title="Search"
              description="Search the configured indexer and add releases to the download queue."
              action={
                <Button asChild size="sm" variant="outline">
                  <a href="#downloads">View queue</a>
                </Button>
              }
            />
            <ReleaseSearch jobs={jobs ?? []} onDownload={download} />
          </div>
        </div>

        {route === 'downloads' && (
          <div className="flex flex-col gap-6">
            <PageHeading
              title="Downloads"
              description="Transfer and processing stages for queued releases."
              action={
                <Button asChild size="sm">
                  <a href="#search">Search releases</a>
                </Button>
              }
            />
            <DownloadQueue
              jobs={jobs}
              error={error}
              onRetry={retry}
              onRefresh={refresh}
              emptyAction={{ label: 'Search releases', href: '#search' }}
            />
          </div>
        )}

        <div hidden={route !== 'settings'} inert={route !== 'settings'}>
          <SettingsPage />
        </div>
      </AppShell>
    </>
  )
}

export default App
