import { useEffect, useState } from 'react'
import { EmptyState, LoadError, LoadingRows } from '@/components/users-shared'
import { errorMessage } from '@/lib/api'
import { authApi, type AuditEntry } from '@/lib/auth-api'
import { formatAge } from '@/lib/format'

const auditLimit = 200

export function UsersAudit() {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    let active = true
    void authApi
      .listAudit(auditLimit, controller.signal)
      .then(items => {
        if (active) setEntries(items)
      })
      .catch(cause => {
        if (active && !controller.signal.aborted) setError(errorMessage(cause))
      })
    return () => {
      active = false
      controller.abort()
    }
  }, [attempt])

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">Administrative actions recorded by the server, newest first.</p>
      {error && (
        <LoadError
          message={error}
          onRetry={() => {
            setError('')
            setAttempt(value => value + 1)
          }}
        />
      )}
      {!entries && !error && <LoadingRows label="Loading activity…" />}
      {entries && entries.length === 0 && (
        <EmptyState title="No recorded activity" description="Administrative changes will appear here." />
      )}

      {entries && entries.length > 0 && (
        <>
          <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border">
            {entries.map((entry, index) => (
              <li key={`${entry.at}-${entry.action}-${index}`} className="grid gap-1 bg-card px-4 py-3 sm:grid-cols-[9rem_1fr] sm:gap-3">
                <time dateTime={entry.at || undefined} title={entry.at} className="text-xs text-muted-foreground">
                  {entry.at ? formatAge(entry.at) : 'Unknown time'}
                </time>
                <div className="min-w-0">
                  <p className="break-words text-sm">
                    <span className="font-medium">{entry.actorName || 'System'}</span> {entry.action}
                    {entry.target && <span className="text-muted-foreground"> · {entry.target}</span>}
                  </p>
                  {entry.outcome && entry.outcome !== 'success' && (
                    <p className="break-words text-xs text-muted-foreground">{entry.outcome}</p>
                  )}
                </div>
              </li>
            ))}
          </ul>
          {entries.length >= auditLimit && (
            <p className="text-xs text-muted-foreground">Showing the most recent {auditLimit} events.</p>
          )}
        </>
      )}
    </div>
  )
}
