// Pure helpers for the iOS companion setup: server addresses, device token names, scopes, and the paste setup payload.

export type ServerURLResult = { url: string } | { error: string }

export type CompanionSetup = { version: 1; serverURL: string; token: string }

// The web app can be served under a base path, and the phone must use that same address.
export function defaultServerURL(origin: string, basePath = '/') {
  const base = basePath.replace(/^\/+|\/+$/g, '')
  const root = origin.replace(/\/+$/, '')
  return base ? `${root}/${base}` : root
}

// Accepts http(s) URLs with a host and no credentials, query, or fragment; drops a trailing slash.
export function normalizeServerURL(value: string): ServerURLResult {
  const trimmed = value.trim()
  if (!trimmed) return { error: 'Enter the address your phone will use to reach this server.' }
  if (trimmed.includes('\\') || [...trimmed].some(character => character.charCodeAt(0) <= 32 || character.charCodeAt(0) === 127)) {
    return { error: 'Remove spaces, control characters, and backslashes from the address.' }
  }
  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch {
    return { error: 'Enter a full address, for example http://192.168.1.10:8080.' }
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return { error: 'Only http:// and https:// addresses work.' }
  }
  if (!parsed.hostname) return { error: 'The address needs a host name or IP address.' }
  if (parsed.username || parsed.password || /^https?:\/\/[^/]*@/i.test(trimmed)) {
    return { error: 'Remove the user name and password from the address.' }
  }
  if (parsed.search || parsed.hash || trimmed.includes('?') || trimmed.includes('#')) {
    return { error: 'Remove the query string and fragment from the address.' }
  }
  if (parsed.port && Number(parsed.port) < 1) return { error: 'Use a port between 1 and 65535.' }
  // Check the supplied path before URL canonicalization can silently remove traversal segments.
  const path = trimmed.replace(/^https?:\/\/[^/]*/i, '').replace(/\/+$/, '')
  if (path.includes('//') || path.split('/').some(segment => segment === '.' || segment === '..') ||
      /%(?:0[0-9a-f]|1[0-9a-f]|20|2e|2f|5c|7f)/i.test(path) || /%(?![0-9a-f]{2})/i.test(path)) {
    return { error: 'Use a server address with a plain base path and no traversal or encoded separators.' }
  }
  return { url: `${parsed.protocol}//${parsed.host}${parsed.pathname.replace(/\/+$/, '')}` }
}

// localhost only resolves on the server itself; a phone needs a LAN address or VPN hostname.
export function isLoopbackServerURL(value: string) {
  try {
    const host = new URL(value).hostname
    return host === 'localhost' || host === '127.0.0.1' || host === '[::1]'
  } catch {
    return false
  }
}

export const companionCapabilities = [
  { permission: 'library.read', label: 'Browse movies, TV, and the calendar' },
  { permission: 'downloads.read', label: 'Follow the Usenet and torrent queues' },
  { permission: 'downloads.write', label: 'Pause and resume downloads' },
  { permission: 'requests.read', label: 'View requests' },
] as const

const permissionOrder = companionCapabilities.map(capability => capability.permission)

// Only permissions the account already holds can be granted, and never a null (all-permissions) scope.
export function companionScopes(permissions: string[], allowDownloadControls: boolean) {
  return permissionOrder.filter(permission =>
    permissions.includes(permission) && (permission !== 'downloads.write' || (allowDownloadControls && permissions.includes('downloads.read'))),
  )
}

export function capabilityLabel(permission: string) {
  return companionCapabilities.find(capability => capability.permission === permission)?.label ?? permission
}

// Token listing is already scoped to the signed-in account; a stable prefix survives account renames.
const devicePrefix = 'iOS · '

export function deviceTokenName(deviceName: string) {
  return `${devicePrefix}${deviceName.trim()}`
}

export function isCompanionToken(name: string) {
  return name.startsWith(devicePrefix)
}

export function companionDeviceLabel(name: string) {
  return isCompanionToken(name) ? name.slice(devicePrefix.length) : name
}

// The setup text the iOS app accepts through Paste connection.
export function companionSetup(serverURL: string, token: string) {
  return JSON.stringify({ version: 1, serverURL, token } satisfies CompanionSetup)
}

// Deep links fill in the address only; the token is never part of a URL.
export function companionDeepLink(serverURL: string) {
  return `constellarr://connect?server=${encodeURIComponent(serverURL)}`
}

export function expiryDateISO(days: number, now = Date.now()) {
  return new Date(now + days * 86_400_000).toISOString()
}

// Shared token expiry wording for the API tokens and companion device lists.
export function expiryLabel(value: string | null, now = Date.now()) {
  if (!value) return 'Never expires'
  const days = Math.ceil((Date.parse(value) - now) / 86_400_000)
  if (!Number.isFinite(days)) return 'No expiry date'
  if (days <= 0) return 'Expired'
  return `Expires in ${days} ${days === 1 ? 'day' : 'days'}`
}
