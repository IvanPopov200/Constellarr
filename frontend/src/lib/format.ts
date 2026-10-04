const sizeUnits = ['B', 'KB', 'MB', 'GB', 'TB']

export function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), sizeUnits.length - 1)
  const value = bytes / 1024 ** unit
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${sizeUnits[unit]}`
}

export function formatAge(published: string) {
  const elapsed = Date.now() - Date.parse(published)
  if (!Number.isFinite(elapsed)) return 'unknown age'
  const minutes = Math.max(0, Math.floor(elapsed / 60_000))
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  const months = Math.floor(days / 30)
  if (months < 12) return `${months}mo ago`
  return `${Math.floor(months / 12)}y ago`
}
