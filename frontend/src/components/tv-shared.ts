import type { Episode, MovieFile, Series } from '@/lib/tv-api'

// Series detail fetches are capped so a large wanted list never floods the API.
export const detailFetchLimit = 40

export const monitorModes = [
  { value: 'all', label: 'All episodes' },
  { value: 'future', label: 'Future episodes' },
  { value: 'missing', label: 'Missing episodes' },
  { value: 'existing', label: 'Existing episodes' },
  { value: 'first', label: 'First season' },
  { value: 'latest', label: 'Latest season' },
  { value: 'none', label: 'None (unmonitored)' },
]

export function monitorModeLabel(value: string) {
  return monitorModes.find((mode) => mode.value === value)?.label ?? value
}

export const activeStatuses = new Set(['searching', 'downloading', 'importing', 'upgrading'])

const statusLabels: Record<string, string> = {
  available: 'Downloaded',
  downloaded: 'Downloaded',
  importing: 'Importing',
  missing: 'Missing',
  wanted: 'Wanted',
  searching: 'Searching',
  downloading: 'Downloading',
  upgrading: 'Upgrading',
  'cutoff-unmet': 'Cutoff unmet',
  failed: 'Failed',
  unmonitored: 'Unmonitored',
}

export const statusTones: Record<string, string> = {
  available: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300',
  downloaded: 'border-emerald-400/25 bg-emerald-400/10 text-emerald-300',
  importing: 'border-primary/30 bg-primary/10 text-primary',
  searching: 'border-primary/30 bg-primary/10 text-primary',
  downloading: 'border-primary/30 bg-primary/10 text-primary',
  upgrading: 'border-primary/30 bg-primary/10 text-primary',
  missing: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  wanted: 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  'cutoff-unmet': 'border-amber-400/25 bg-amber-400/10 text-amber-300',
  unmonitored: 'border-border bg-muted/60 text-muted-foreground',
}

export function strings(value: string[] | null | undefined) {
  return (value ?? []).filter((item) => typeof item === 'string' && item.trim().length > 0)
}

export function splitList(value: string) {
  return value
    .split(/[,\n]/)
    .map((item) => item.trim())
    .filter(Boolean)
}

export function availableFiles(episode: Episode): MovieFile[] {
  return (episode.files ?? []).filter((file) => !file.missing)
}

export function bestFile(episode: Episode): MovieFile | null {
  const files = availableFiles(episode)
  if (files.length === 0) return null
  return files.reduce((best, file) => (file.score >= best.score ? file : best))
}

// Unknown stays unknown: a series with no episodes yet reports no progress instead of a zero total.
export function seriesProgress(series: Series) {
  const total = Math.max(0, series.total || 0)
  const downloaded = Math.min(Math.max(0, series.downloaded || 0), total)
  const wanted = Math.max(0, series.wanted || 0)
  const percent = total > 0 ? Math.round((downloaded / total) * 100) : null
  return { downloaded, total, wanted, percent }
}

function seriesState(series: Series) {
  if (series.status) return series.status
  if (series.total > 0 && series.downloaded >= series.total) return 'downloaded'
  if (series.wanted > 0) return 'wanted'
  if (series.total > 0) return 'missing'
  return series.monitored ? 'wanted' : 'unmonitored'
}

// Monitored series that still need an episode file or a cutoff upgrade.
export function seriesNeedsAttention(series: Series) {
  return (
    series.monitored &&
    (series.wanted > 0 || (series.total > 0 && series.downloaded < series.total) || series.status === 'cutoff-unmet')
  )
}

export function stateKey(series: Series) {
  return seriesState(series).trim().toLowerCase().replace(/_/g, '-')
}

export function statusLabel(status: string) {
  return statusLabels[status] ?? (status ? status.replace(/[-_]/g, ' ') : 'Unknown')
}

export function ratingText(rating: number | null | undefined) {
  return typeof rating === 'number' && rating > 0 ? rating.toFixed(1) : null
}

// Date-only values keep their written day; they are never shifted by a timezone.
export function airDateValue(value: string | null | undefined) {
  const text = (value ?? '').trim()
  if (!text) return null
  const match = text.match(/^(\d{4})-(\d{2})-(\d{2})/)
  const date = match
    ? new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
    : new Date(text)
  if (Number.isNaN(date.getTime())) return null
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime()
}

export function todayStart() {
  const now = new Date()
  return new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()
}

export function formatDate(value: number | null) {
  if (value === null) return 'Unknown'
  return new Date(value).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

export function formatDateTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? 'Unknown time' : date.toLocaleString()
}

export function seasonLabel(season: number) {
  return season === 0 ? 'Specials' : `Season ${season}`
}

// A monitored episode that needs a file, or a file that still sits below the profile cutoff.
export function episodeWanted(episode: Episode) {
  if (!episode.monitored) return false
  if (episode.status === 'cutoff-unmet') return true
  return availableFiles(episode).length === 0
}

export function isAired(episode: Episode) {
  const at = airDateValue(episode.airDate)
  return at !== null && at <= todayStart()
}

export type WantedRow = { series: Series; episode: Episode }

// Missing files and below-cutoff upgrades, minus future episodes, active downloads, and unmonitored series.
export function wantedRows(series: Series[], details: Record<string, Series>): WantedRow[] {
  const today = todayStart()
  const output: WantedRow[] = []
  for (const item of series) {
    if (!item.monitored) continue
    const detail = details[item.id]
    if (!detail) continue
    for (const episode of detail.episodes ?? []) {
      if (!episodeWanted(episode) || activeStatuses.has(episode.status)) continue
      const at = airDateValue(episode.airDate)
      if (at !== null && at > today) continue
      output.push({ series: item, episode })
    }
  }
  // Most recently aired first; episodes that still wait for a date come last.
  return output.sort((a, b) => {
    const left = airDateValue(a.episode.airDate)
    const right = airDateValue(b.episode.airDate)
    if (left === null && right === null) {
      if (a.episode.season !== b.episode.season) return a.episode.season - b.episode.season
      return a.episode.number - b.episode.number
    }
    if (left === null) return 1
    if (right === null) return -1
    return right - left
  })
}

export function tagsOf(series: Series) {
  return strings(series.tags)
}

export const templateTokens = [
  { token: '{title}', label: 'Series title' },
  { token: '{year}', label: 'Series year' },
  { token: '{imdbId}', label: 'IMDb ID' },
  { token: '{quality}', label: 'Release quality' },
  { token: '{season}', label: 'Season number' },
  { token: '{episode}', label: 'Episode number' },
  { token: '{episodeCode}', label: 'S01E02 episode code' },
  { token: '{episodeTitle}', label: 'Episode title' },
  { token: '{original}', label: 'Original release name' },
  { token: '{part}', label: 'Part number for split files' },
]
