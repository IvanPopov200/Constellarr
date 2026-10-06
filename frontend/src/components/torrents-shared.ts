import { formatBytes } from '@/lib/format'
import type { TorrentJob, TorrentStatus } from '@/lib/torrents-api'

export type TorrentDisplayStatus = TorrentStatus | 'extracting'

export const statusLabels: Record<TorrentDisplayStatus, string> = {
  queued: 'Queued',
  metadata: 'Fetching metadata',
  checking: 'Checking',
  downloading: 'Downloading',
  seeding: 'Seeding',
  paused: 'Paused',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
  extracting: 'Extracting',
}

// displayStatus reports archive extraction without changing the engine status.
export function displayStatus(job: TorrentJob): TorrentDisplayStatus {
  const state = job.processing?.state
  if (state === 'pending' || state === 'running') return 'extracting'
  return job.status
}

export function isExtracting(job: TorrentJob) {
  const state = job.processing?.state
  return state === 'pending' || state === 'running'
}

export function statusVariant(status: TorrentDisplayStatus): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'failed':
      return 'destructive'
    case 'seeding':
    case 'completed':
      return 'secondary'
    case 'downloading':
    case 'extracting':
      return 'default'
    default:
      return 'outline'
  }
}

export function progressPercent(job: TorrentJob) {
  if (job.status === 'completed' || job.status === 'seeding') return 100
  if (job.piecesTotal <= 0) return 0
  return Math.min(100, Math.round((job.piecesDone / job.piecesTotal) * 100))
}

export function formatRate(bytesPerSecond: number) {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return '—'
  return `${formatBytes(bytesPerSecond)}/s`
}

export function formatETA(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—'
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`
  return `${Math.round(seconds / 86400)}d`
}

export function formatRatio(ratio: number) {
  return Number.isFinite(ratio) && ratio > 0 ? ratio.toFixed(2) : '0.00'
}

export function formatSeedLimit(limit: number, minutes: number) {
  const ratio = limit > 0 ? `ratio ${limit}` : 'no ratio limit'
  const time = minutes > 0 ? `${minutes} min` : 'no time limit'
  return `${ratio} · ${time}`
}

export function magnetIsValid(value: string) {
  return /^magnet:\?[^]*xt=urn:btih:[a-z0-9]+/i.test(value.trim())
}

export function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('The torrent file could not be read.'))
    reader.onload = () => {
      const result = typeof reader.result === 'string' ? reader.result : ''
      const separator = result.indexOf(',')
      resolve(separator >= 0 ? result.slice(separator + 1) : result)
    }
    reader.readAsDataURL(file)
  })
}

export function parseCategories(value: string): number[] | null {
  const trimmed = value.trim()
  if (!trimmed) return []
  const parts = trimmed.split(',').map((part) => part.trim())
  const categories: number[] = []
  for (const part of parts) {
    const category = Number(part)
    if (!Number.isInteger(category) || category < 1 || category > 1_000_000) return null
    categories.push(category)
  }
  return categories
}
