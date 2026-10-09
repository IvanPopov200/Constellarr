export const routeLabels = {
  overview: 'Overview',
  requests: 'Requests',
  movies: 'Movies',
  'tv-shows': 'TV Shows',
  music: 'Music',
  subtitles: 'Subtitles',
  calendar: 'Calendar',
  usenet: 'Usenet',
  torrents: 'Torrents',
  connections: 'Connections',
  storage: 'Storage & Paths',
  users: 'Users & Access',
  companion: 'Connect iOS',
  migration: 'Migration',
  backups: 'Backups & Imports',
  system: 'System',
} as const

export type Route = keyof typeof routeLabels

export const routePermissions: Partial<Record<Route, string>> = {
  requests: 'requests.read',
  movies: 'library.read',
  'tv-shows': 'library.read',
  music: 'library.read',
  subtitles: 'subtitles.read',
  calendar: 'library.read',
  usenet: 'downloads.read',
  torrents: 'downloads.read',
  connections: 'settings.read',
  storage: 'settings.read',
  migration: 'migration.manage',
  backups: 'backups.manage',
  system: 'monitoring.read',
}
