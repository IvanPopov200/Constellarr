import { BackendStatus } from '@/components/backend-status'
import { DownloadQueue } from '@/components/download-queue'
import { ReleaseSearch } from '@/components/release-search'
import { SourcesCard } from '@/components/sources-card'
import { useDownloads } from '@/lib/use-downloads'

function ConstellationMark() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" className="size-9 shrink-0 text-primary">
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

function App() {
  const { jobs, error, refresh, download, retry } = useDownloads()

  return (
    <main className="mx-auto flex min-h-svh w-full max-w-4xl flex-col gap-6 px-4 py-8 sm:py-12">
      <header className="flex items-center gap-3">
        <ConstellationMark />
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Constellarr</h1>
          <p className="text-sm text-muted-foreground">Self-hosted Usenet search and downloads</p>
        </div>
      </header>

      <ReleaseSearch jobs={jobs ?? []} onDownload={download} />
      <DownloadQueue jobs={jobs} error={error} onRetry={retry} onRefresh={refresh} />

      <div className="grid gap-6 lg:grid-cols-2">
        <SourcesCard />
        <BackendStatus />
      </div>
    </main>
  )
}

export default App
