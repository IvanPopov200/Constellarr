export type ListEnvelope = 'events' | 'backups'

// List routes answer with a named envelope; a mismatched or bare payload must fail loudly instead of rendering an empty list.
export function unwrapList<T>(payload: unknown, key: ListEnvelope): T[] {
  const value =
    typeof payload === 'object' && payload !== null ? (payload as Record<string, unknown>)[key] : undefined
  if (!Array.isArray(value)) {
    throw new Error(`The server did not return a "${key}" list for this request.`)
  }
  return value as T[]
}
