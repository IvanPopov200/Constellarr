import { unwrapList } from './operations-envelope.ts'

// Runs with `node --test src/lib/operations-envelope.test.ts`; the fixtures mirror the Go handlers' envelopes.

function expectRejected(run: () => unknown, key: string) {
  try {
    run()
  } catch (cause) {
    if (cause instanceof Error && cause.message.includes(`"${key}" list`)) return
    throw new Error(`expected a missing "${key}" list error, got ${String(cause)}`, { cause })
  }
  throw new Error(`expected the "${key}" list to be rejected`)
}

const events = [{ id: 1, kind: 'import_error' }]
const unwrapped = unwrapList<{ id: number }>({ events }, 'events')
if (unwrapped !== events) throw new Error('the events envelope must be returned unchanged')
if (unwrapList({ events: [] }, 'events').length !== 0) throw new Error('an empty events envelope must unwrap')

const backups = [{ id: '20260101T000000Z-00000001' }]
if (unwrapList<{ id: string }>({ backups }, 'backups') !== backups) {
  throw new Error('the backups envelope must be returned unchanged')
}

// A bare array is what the interface used to expect; it must now fail instead of rendering an empty list.
expectRejected(() => unwrapList([{ id: 1 }], 'backups'), 'backups')
expectRejected(() => unwrapList([], 'events'), 'events')
expectRejected(() => unwrapList({ backups: [] }, 'events'), 'events')
expectRejected(() => unwrapList({ events: { id: 1 } }, 'events'), 'events')
expectRejected(() => unwrapList({ generatedAt: '2026-01-01' }, 'events'), 'events')
expectRejected(() => unwrapList(null, 'backups'), 'backups')
expectRejected(() => unwrapList('nope', 'backups'), 'backups')
