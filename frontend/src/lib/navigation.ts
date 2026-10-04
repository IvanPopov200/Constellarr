export const routeLabels = {
  overview: 'Overview',
  requests: 'Requests',
  movies: 'Movies',
  'tv-shows': 'TV Shows',
  music: 'Music',
  subtitles: 'Subtitles',
  usenet: 'Usenet',
  torrents: 'Torrents',
  connections: 'Connections',
  storage: 'Storage & Paths',
  users: 'Users & Access',
  system: 'System',
} as const

export type Route = keyof typeof routeLabels
