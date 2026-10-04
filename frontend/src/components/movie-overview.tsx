import { useEffect, useState } from 'react'
import { ArrowRight, Film, LoaderCircle, Star } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { errorMessage } from '@/lib/api'
import { moviesApi, type Movie } from '@/lib/movies-api'

export function MovieOverview() {
  const [movies, setMovies] = useState<Movie[] | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    let fetching = false
    async function refresh() {
      if (fetching || document.visibilityState !== 'visible') return
      fetching = true
      try {
        const result = await moviesApi.list(controller.signal)
        if (!controller.signal.aborted) { setMovies(result); setError('') }
      } catch (cause) {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      } finally { fetching = false }
    }
    void refresh()
    const timer = window.setInterval(refresh, 15_000)
    window.addEventListener('movies-changed', refresh)
    return () => { controller.abort(); window.clearInterval(timer); window.removeEventListener('movies-changed', refresh) }
  }, [])

  const downloaded = movies?.filter(movie => (movie.files?.length ?? 0) > 0 && movie.status !== 'missing') ?? []
  const wanted = movies?.filter(movie => movie.monitored && (movie.files?.length ?? 0) === 0).length ?? 0

  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>Movie library</CardTitle>
        <CardDescription>{movies ? `${downloaded.length} available · ${wanted} monitored and wanted` : 'Your downloaded movies and monitored releases.'}</CardDescription>
        <CardAction><Button asChild size="sm" variant="ghost"><a href="#movies">View library<ArrowRight /></a></Button></CardAction>
      </CardHeader>
      <CardContent>
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        {!movies && !error && <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />Loading movie library…</p>}
        {movies && downloaded.length === 0 && <p className="text-sm text-muted-foreground">Imported movies will appear here. Add movies to monitor their releases.</p>}
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {downloaded.slice(0,4).map(movie => <a key={movie.id} href="#movies" className="flex min-w-0 items-center gap-3 rounded-lg border border-border p-3 transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <div className="relative flex h-20 w-14 shrink-0 items-center justify-center overflow-hidden rounded-md bg-muted">
              <Film className="size-5 text-muted-foreground" aria-hidden="true" />
              {movie.metadata.poster && <img src={movie.metadata.poster} alt="" loading="lazy" className="absolute inset-0 size-full object-cover" onError={event => { event.currentTarget.hidden = true }} />}
            </div>
            <div className="min-w-0 space-y-1">
              <p className="truncate text-sm font-medium">{movie.metadata.title}</p>
              <p className="text-xs text-muted-foreground">{movie.metadata.year || 'Year unknown'} · {movie.files?.[0]?.quality || 'Quality unknown'}</p>
              {movie.metadata.rating !== null && <p className="flex items-center gap-1 text-xs text-muted-foreground"><Star className="size-3" />{movie.metadata.rating.toFixed(1)} IMDb</p>}
            </div>
          </a>)}
        </div>
      </CardContent>
    </Card>
  )
}
