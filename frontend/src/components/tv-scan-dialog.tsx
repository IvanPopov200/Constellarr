import { useEffect, useRef, useState } from 'react'
import { CheckIcon, FolderSearchIcon, LoaderCircleIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DialogShell, EmptyState, ErrorNote, Notice, Select } from '@/components/tv-ui'
import { errorMessage } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { splitList } from '@/components/tv-shared'
import { tvApi, type RootFolder, type ScanCandidate, type Series } from '@/lib/tv-api'

type Draft = { seriesId: string; season: string; episodes: string }

function draftFor(candidate: ScanCandidate): Draft {
  return {
    seriesId: '',
    season: candidate.season > 0 ? String(candidate.season) : '1',
    episodes: (candidate.episodes ?? []).join(', '),
  }
}

export function ScanDialog({
  active,
  series,
  roots,
  importMode,
  onClose,
  onImported,
}: {
  active: boolean
  series: Series[]
  roots: RootFolder[]
  importMode: string
  onClose: () => void
  onImported: (series: Series) => void
}) {
  const [rootId, setRootId] = useState(roots[0]?.id ?? '')
  const [candidates, setCandidates] = useState<ScanCandidate[] | null>(null)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const [importedPaths, setImportedPaths] = useState<Record<string, string>>({})
  const [scanning, setScanning] = useState(false)
  const [importing, setImporting] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const controller = useRef<AbortController | null>(null)

  useEffect(() => () => controller.current?.abort(), [])

  const scan = async () => {
    if (!rootId) {
      setError('Choose a root folder to scan.')
      return
    }
    controller.current?.abort()
    const request = new AbortController()
    controller.current = request
    setScanning(true)
    setCandidates(null)
    setDrafts({})
    setError('')
    setNotice('')
    try {
      const found = await tvApi.scan(rootId, request.signal)
      if (!request.signal.aborted) setCandidates(found)
    } catch (cause) {
      if (!request.signal.aborted) setError(errorMessage(cause))
    } finally {
      if (!request.signal.aborted) setScanning(false)
    }
  }

  const importCandidate = async (candidate: ScanCandidate) => {
    const draft = drafts[candidate.path] ?? draftFor(candidate)
    const season = Number(draft.season)
    const episodes = splitList(draft.episodes)
      .map((value) => Number(value))
      .filter((value) => Number.isInteger(value) && value > 0)
    if (!draft.seriesId) {
      setError('Choose the series for this file before importing.')
      return
    }
    if (!Number.isInteger(season) || season < 0) {
      setError('Enter a season number of 0 or more (0 is specials).')
      return
    }
    if (episodes.length === 0) {
      setError('Enter the episode numbers in this file, separated by commas.')
      return
    }
    setImporting(candidate.path)
    setError('')
    setNotice('')
    try {
      const target = await tvApi.importFiles({
        rootId,
        path: candidate.path,
        seriesId: draft.seriesId,
        season,
        episodes,
      })
      setImportedPaths((previous) => ({ ...previous, [candidate.path]: target.metadata.title || target.id }))
      onImported(target)
      setNotice(
        `Imported ${candidate.path} into ${target.metadata.title || 'the series'} (${season === 0 ? 'specials' : `season ${season}`}).`,
      )
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setImporting('')
    }
  }

  return (
    <DialogShell
      active={active}
      title="Scan TV library"
      description="Scanning is read-only. Constellarr suggests a match, but series, season, and episode numbers stay explicit."
      onClose={onClose}
    >
      <div className="space-y-4">
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-52 flex-1 space-y-2">
            <label htmlFor="tv-scan-root" className="text-sm font-medium">
              Root folder
            </label>
            <Select
              id="tv-scan-root"
              className="w-full"
              value={rootId}
              onChange={(event) => setRootId(event.target.value)}
            >
              {roots.length === 0 && <option value="">No TV root folders configured</option>}
              {roots.map((root) => (
                <option key={root.id || root.path} value={root.id}>
                  {root.path}
                </option>
              ))}
            </Select>
          </div>
          <Button size="sm" disabled={scanning || !rootId} onClick={() => void scan()}>
            {scanning ? (
              <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
            ) : (
              <FolderSearchIcon data-icon="inline-start" />
            )}
            {scanning ? 'Scanning…' : candidates === null ? 'Scan folder' : 'Scan again'}
          </Button>
        </div>

        <p className="text-xs text-muted-foreground">
          Imports follow the configured import mode ({importMode || 'copy'}). Existing files and other episodes are
          preserved.
        </p>

        {error && (
          <ErrorNote onRetry={candidates === null ? () => void scan() : undefined}>{error}</ErrorNote>
        )}
        {notice && <Notice>{notice}</Notice>}

        {candidates === null ? (
          <EmptyState>
            Choose a TV root folder and scan to list video files that are not matched to an episode yet.
          </EmptyState>
        ) : candidates.length === 0 ? (
          <EmptyState>No TV files found in this folder.</EmptyState>
        ) : (
          <ul className="space-y-2">
            {candidates.map((candidate) => {
              const draft = drafts[candidate.path] ?? draftFor(candidate)
              const suggested = series.find((item) => item.id === candidate.matchedSeriesId)
              const importedAs = importedPaths[candidate.path]
              const change = (next: Partial<Draft>) =>
                setDrafts((previous) => ({ ...previous, [candidate.path]: { ...draft, ...next } }))
              return (
                <li key={candidate.path} className="space-y-2 rounded-lg border border-border p-3">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <p className="text-sm font-medium">
                        {candidate.title || 'Unknown title'}
                        {candidate.year > 0 ? ` (${candidate.year})` : ''}
                      </p>
                      <p className="text-xs break-all text-muted-foreground">{candidate.path}</p>
                      <p className="text-xs text-muted-foreground">
                        {formatBytes(candidate.size)}
                        {candidate.quality ? ` · ${candidate.quality}` : ''}
                        {candidate.imdbId ? ` · ${candidate.imdbId}` : ''}
                        {candidate.airDate ? ` · airs ${candidate.airDate}` : ''}
                      </p>
                    </div>
                    {importedAs ? (
                      <Badge variant="outline" className="border-emerald-400/25 bg-emerald-400/10 text-emerald-300">
                        Imported into {importedAs}
                      </Badge>
                    ) : suggested ? (
                      <Badge variant="outline">Suggested: {suggested.metadata.title}</Badge>
                    ) : (
                      <Badge variant="outline" className="text-amber-300">
                        No match: choose a series
                      </Badge>
                    )}
                  </div>
                  {candidate.error && <p className="text-xs text-destructive">{candidate.error}</p>}
                  {importedAs && (
                    <p className="text-xs text-muted-foreground">
                      This file is imported into the library. Scan again to refresh the list.
                    </p>
                  )}
                  <div className="grid gap-2 sm:grid-cols-[minmax(0,2fr)_6rem_minmax(0,1fr)_auto] sm:items-end">
                    <div className="space-y-1">
                      <label htmlFor={`tv-scan-series-${candidate.path}`} className="text-xs text-muted-foreground">
                        Series
                      </label>
                      <Select
                        id={`tv-scan-series-${candidate.path}`}
                        className="h-8 w-full"
                        value={draft.seriesId}
                        onChange={(event) => change({ seriesId: event.target.value })}
                      >
                        <option value="">Choose series…</option>
                        {series.map((item) => (
                          <option key={item.id} value={item.id}>
                            {item.metadata.title}
                            {item.metadata.year > 0 ? ` (${item.metadata.year})` : ''}
                          </option>
                        ))}
                      </Select>
                      {suggested && draft.seriesId !== suggested.id && (
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => change({ seriesId: suggested.id })}
                        >
                          Use suggested match
                        </Button>
                      )}
                    </div>
                    <div className="space-y-1">
                      <label htmlFor={`tv-scan-season-${candidate.path}`} className="text-xs text-muted-foreground">
                        Season
                      </label>
                      <Input
                        id={`tv-scan-season-${candidate.path}`}
                        type="number"
                        min="0"
                        className="h-8"
                        value={draft.season}
                        onChange={(event) => change({ season: event.target.value })}
                      />
                    </div>
                    <div className="space-y-1">
                      <label htmlFor={`tv-scan-episodes-${candidate.path}`} className="text-xs text-muted-foreground">
                        Episodes
                      </label>
                      <Input
                        id={`tv-scan-episodes-${candidate.path}`}
                        className="h-8"
                        placeholder="1, 2, 3"
                        value={draft.episodes}
                        onChange={(event) => change({ episodes: event.target.value })}
                      />
                    </div>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={importing !== '' || Boolean(importedAs)}
                      aria-label={`Import ${candidate.path} into ${
                        series.find((item) => item.id === draft.seriesId)?.metadata.title || 'the chosen series'
                      }`}
                      onClick={() => void importCandidate(candidate)}
                    >
                      {importing === candidate.path ? (
                        <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
                      ) : (
                        <CheckIcon data-icon="inline-start" />
                      )}
                      {importedAs ? 'Imported' : 'Import'}
                    </Button>
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </DialogShell>
  )
}
