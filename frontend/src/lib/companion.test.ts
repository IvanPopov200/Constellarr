import {
  capabilityLabel,
  companionDeepLink,
  companionDeviceLabel,
  companionScopes,
  companionSetup,
  defaultServerURL,
  deviceTokenName,
  expiryDateISO,
  expiryLabel,
  isCompanionToken,
  isLoopbackServerURL,
  normalizeServerURL,
} from './companion.ts'

// Runs with `node --test src/lib/companion.test.ts`; covers the pure helpers behind the iOS companion setup page.

function expectURL(value: string, expected: string) {
  const result = normalizeServerURL(value)
  if (!('url' in result)) {
    throw new Error(`expected ${JSON.stringify(value)} to normalize, got: ${result.error}`)
  }
  if (result.url !== expected) {
    throw new Error(`expected ${JSON.stringify(value)} to become ${expected}, got ${result.url}`)
  }
}

function expectRejected(value: string, fragment: string) {
  const result = normalizeServerURL(value)
  if ('url' in result) {
    throw new Error(`expected ${JSON.stringify(value)} to be rejected, got ${result.url}`)
  }
  if (!result.error.includes(fragment)) {
    throw new Error(
      `expected the rejection of ${JSON.stringify(value)} to mention ${JSON.stringify(fragment)}, got ${JSON.stringify(result.error)}`,
    )
  }
}

function expectEqual(actual: unknown, expected: unknown, what: string) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`${what}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`)
  }
}

expectURL('  http://192.168.1.10:8080/  ', 'http://192.168.1.10:8080')
expectURL('HTTP://HOST/App/', 'http://host/App')
expectURL('http://host/app///', 'http://host/app')
expectURL('https://media.example.com:8443/constellarr/', 'https://media.example.com:8443/constellarr')
expectURL('http://host:80/', 'http://host')

expectRejected('', 'address')
expectRejected('media.example.com:8080', 'http')
expectRejected('ftp://host', 'http')
expectRejected('constellarr://connect?server=http%3A%2F%2Fhost', 'http')
expectRejected('http://host:99999', 'full address')
expectRejected('http://user@host:8080', 'user name')
expectRejected('http://user:secret@host:8080', 'user name')
expectRejected('http://host:8080/?x=1', 'query')
expectRejected('http://host:8080/#setup', 'fragment')

expectURL(defaultServerURL('http://server:8080', '/'), 'http://server:8080')
expectURL(defaultServerURL('http://server:8080/', '/constellarr/'), 'http://server:8080/constellarr')
expectURL(defaultServerURL('https://media.example.com', ''), 'https://media.example.com')

if (!isLoopbackServerURL('http://localhost:8080')) throw new Error('localhost must be reported as loopback')
if (!isLoopbackServerURL('http://127.0.0.1')) throw new Error('127.0.0.1 must be reported as loopback')
if (!isLoopbackServerURL('http://[::1]:8080')) throw new Error('::1 must be reported as loopback')
if (isLoopbackServerURL('http://192.168.1.10:8080')) throw new Error('a LAN address is not loopback')
if (isLoopbackServerURL('http://localhost.example.com')) throw new Error('a host that starts with localhost is not loopback')
if (isLoopbackServerURL('not a url')) throw new Error('an unparsable address is not loopback')

const viewer = ['library.read', 'downloads.read', 'requests.read']
expectEqual(companionScopes(viewer, true), viewer, 'the three readable scopes are granted in order')
expectEqual(companionScopes([], true), [], 'an account with no permissions gets no scopes')
expectEqual(companionScopes(['users.manage', 'settings.write'], true), [], 'unrelated permissions are never delegated')
expectEqual(
  companionScopes([...viewer, 'downloads.write'], true),
  ['library.read', 'downloads.read', 'downloads.write', 'requests.read'],
  'download controls add downloads.write between the read scopes',
)
expectEqual(
  companionScopes([...viewer, 'downloads.write'], false),
  viewer,
  'unchecking download controls removes downloads.write',
)
expectEqual(
  companionScopes(['downloads.write'], true),
  [],
  'download controls require permission to read the queue',
)

expectEqual(capabilityLabel('downloads.write'), 'Pause and resume downloads', 'known scopes get plain wording')
expectEqual(capabilityLabel('users.manage'), 'users.manage', 'unknown scopes fall back to their key')

expectEqual(deviceTokenName(' iPhone '), 'iOS · iPhone', 'device names are trimmed')
if (!isCompanionToken('iOS · iPhone')) throw new Error('device tokens remain recognizable after an account rename')
if (isCompanionToken('API script')) throw new Error('an ordinary token must not match')
expectEqual(companionDeviceLabel('iOS · iPhone'), 'iPhone', 'the device name is shown without its prefix')
expectEqual(companionDeviceLabel('API script'), 'API script', 'other token names are unchanged')
expectEqual([...deviceTokenName('a'.repeat(58))].length, 64, 'the longest device name fits the API token name limit')

const setup = companionSetup('http://192.168.1.10:8080', 'test-device-token')
expectEqual(
  JSON.parse(setup),
  { version: 1, serverURL: 'http://192.168.1.10:8080', token: 'test-device-token' },
  'the setup payload is the agreed JSON shape',
)
if (!setup.startsWith('{"version":1,')) throw new Error('the setup payload must lead with its version')

const link = companionDeepLink('http://192.168.1.10:8080')
expectEqual(link, 'constellarr://connect?server=http%3A%2F%2F192.168.1.10%3A8080', 'the deep link carries the encoded address')
if (link.includes('secret') || link.toLowerCase().includes('token')) {
  throw new Error('the deep link must never carry a token')
}
expectEqual(
  companionDeepLink('https://media.example.com/constellarr'),
  'constellarr://connect?server=https%3A%2F%2Fmedia.example.com%2Fconstellarr',
  'a base path survives deep-link encoding',
)

expectEqual(expiryDateISO(30, Date.UTC(2026, 0, 1)), '2026-01-31T00:00:00.000Z', 'expiry is counted from now')

expectEqual(expiryLabel(null), 'Never expires', 'a token without expiry never expires')
expectEqual(
  expiryLabel(new Date(Date.UTC(2026, 0, 31)).toISOString(), Date.UTC(2026, 0, 1)),
  'Expires in 30 days',
  'remaining whole days are counted up',
)
expectEqual(expiryLabel(new Date(Date.UTC(2026, 0, 1)).toISOString(), Date.UTC(2026, 0, 2)), 'Expired', 'a past expiry is expired')
expectEqual(expiryLabel('not a date'), 'No expiry date', 'an unreadable expiry is reported plainly')

expectRejected('http://host:0', 'port')
expectRejected('http://host/base/../other', 'base path')
expectRejected('http://host/base/%2e%2e', 'base path')
expectRejected('http://host/a%2fb', 'base path')
expectRejected('http://host/a b', 'spaces')
expectRejected('http://@host', 'user name')
expectRejected('http://host/?', 'query')
