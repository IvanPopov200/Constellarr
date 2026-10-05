import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { DatabaseBackup, Download, HardDriveDownload, LoaderCircle, RefreshCw, RotateCcw, Save, ShieldAlert, Trash2, Upload } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Checkbox, DialogShell, EmptyState, ErrorNote, LoadingNote, Notice } from '@/components/tv-ui'
import { useAuth } from '@/lib/auth-context'
import { errorMessage } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { maxUploadBytes, operationsApi, type Backup, type BackupConfig, type RestorePlan, type RestoreResult } from '@/lib/operations-api'

const originLabels: Record<Backup['origin'], string> = {
  manual: 'Manual',
  scheduled: 'Scheduled',
  rollback: 'Rollback copy',
  imported: 'Imported',
}

type DialogState =
  | { kind: 'create' }
  | { kind: 'delete'; backup: Backup }
  | { kind: 'restore'; backup: Backup }
  | null

export function BackupPanel() {
  const [backups, setBackups] = useState<Backup[] | null>(null)
  const [config, setConfig] = useState<BackupConfig | null>(null)
  const [draft, setDraft] = useState<BackupConfig | null>(null)
  const [dialog, setDialog] = useState<DialogState>(null)
  const [plan, setPlan] = useState<RestorePlan | null>(null)
  const [result, setResult] = useState<RestoreResult | null>(null)
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState<'create' | 'import' | 'config' | 'delete' | 'restore' | 'plan' | null>(null)
  const [reconnecting, setReconnecting] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)
  const { can, refresh: refreshAuth } = useAuth()
  const canManageBackups = can('backups.manage')
  // Restoring replaces accounts, sessions and stored credentials, so it needs full administration.
  const canRestore = canManageBackups && can('users.manage')

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const [list, settings] = await Promise.all([operationsApi.backups(signal), operationsApi.backupConfig(signal)])
    if (signal?.aborted) return
    setBackups(list)
    setConfig(settings)
    setDraft(settings)
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    Promise.all([operationsApi.backups(controller.signal), operationsApi.backupConfig(controller.signal)])
      .then(([list, settings]) => {
        if (controller.signal.aborted) return
        setBackups(list)
        setConfig(settings)
        setDraft(settings)
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [])

  async function run(action: 'create' | 'import' | 'config' | 'delete', task: () => Promise<void>) {
    setBusy(action)
    setError('')
    setNotice('')
    try {
      await task()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function createBackup() {
    await run('create', async () => {
      const backup = await operationsApi.createBackup()
      setDialog(null)
      setNotice(`Backup ${backup.id} created with ${formatBytes(backup.bytes)}.`)
      await refresh()
    })
  }

  async function deleteBackup(backup: Backup) {
    await run('delete', async () => {
      await operationsApi.deleteBackup(backup.id)
      setDialog(null)
      setNotice(`Backup ${backup.id} deleted.`)
      await refresh()
    })
  }

  async function importBackupFile(file: File) {
    await run('import', async () => {
      const backup = await operationsApi.importBackup(file)
      setNotice(`Backup ${backup.id} imported and validated.`)
      await refresh()
    })
    if (fileInput.current) fileInput.current.value = ''
  }

  async function saveConfig(event: FormEvent) {
    event.preventDefault()
    if (!draft) return
    await run('config', async () => {
      const saved = await operationsApi.saveBackupConfig(draft)
      setConfig(saved)
      setDraft(saved)
      setNotice('Backup schedule saved.')
    })
  }

  async function openRestore(backup: Backup) {
    setDialog({ kind: 'restore', backup })
    setPlan(null)
    setResult(null)
    setConfirm('')
    setBusy('plan')
    setError('')
    try {
      setPlan(await operationsApi.previewRestore(backup.id))
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  // waitForReconnect polls until the restarted server answers, then reloads the session and data.
  async function waitForReconnect() {
    setReconnecting(true)
    const deadline = Date.now() + 120_000
    while (Date.now() < deadline) {
      await new Promise((resolve) => window.setTimeout(resolve, 2000))
      try {
        await operationsApi.status()
        await refreshAuth()
        await refresh()
        setReconnecting(false)
        setNotice('The server is back online and the interface reconnected.')
        return
      } catch {
        // The server is still restarting; keep waiting until the deadline.
      }
    }
    setReconnecting(false)
    setError('The server did not come back yet. Reload this page once it is available.')
  }

  async function restoreBackup(backup: Backup) {
    if (!canRestore) {
      setError('Restoring needs full administration: backups.manage and users.manage.')
      return
    }
    setBusy('restore')
    setError('')
    setNotice('')
    try {
      const restored = await operationsApi.restore(backup.id)
      setResult(restored)
      if (restored.autoRestart) {
        setNotice(`Database restored from ${restored.backupId}. The server is restarting and reconnecting.`)
        await waitForReconnect()
        return
      }
      setNotice(`Database restored from ${restored.backupId}. Restart the server to reconnect every service.`)
      await refresh()
    } catch (cause) {
      setError(errorMessage(cause))
      // A failure after submission may be the automatic restart; wait only when the server is really gone.
      if (plan?.autoRestart) {
        try {
          await operationsApi.status()
        } catch {
          await waitForReconnect()
        }
      }
    } finally {
      setBusy(null)
    }
  }

  if (loading && !backups) {
    return <LoadingNote>Loading backups…</LoadingNote>
  }
  if (!backups || !config || !draft) {
    return <ErrorNote>{error || 'Backups could not be loaded.'}</ErrorNote>
  }
  const dirty = JSON.stringify(draft) !== JSON.stringify(config)

  return (
    <div className="space-y-5">
      {error && <ErrorNote onRetry={() => void refresh()}>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}
      {!canRestore && (
        <Notice>
          Restoring a backup replaces accounts, sessions, and stored credentials, so this server requires full
          administration ({canManageBackups ? 'users.manage is missing' : 'backups.manage is missing'}) and a signed-in
          browser session. The buttons below stay read-only until then.
        </Notice>
      )}
      {reconnecting && (
        <p role="status" className="flex items-center gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
          <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" />
          The server is restarting after the restore. Waiting for it to reconnect…
        </p>
      )}

      <form onSubmit={saveConfig}>
        <Card className="shadow-none">
          <CardHeader className="border-b border-border">
            <CardTitle className="flex items-center gap-2">
              <DatabaseBackup className="size-4 text-muted-foreground" />
              Backup schedule
            </CardTitle>
            <CardDescription>
              Backups contain the PostgreSQL database, including settings and stored credentials, plus configuration
              files. Media, downloads, and posters are excluded.
            </CardDescription>
          </CardHeader>
          <CardContent className="gap-4">
            <Checkbox
              id="backup-schedule"
              label="Create backups automatically"
              description="One backup per interval; older automatic and manual backups are removed by retention."
              checked={draft.scheduleEnabled}
              onChange={(scheduleEnabled) => setDraft({ ...draft, scheduleEnabled })}
            />
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-2">
                <label htmlFor="backup-interval" className="text-sm font-medium">
                  Interval (hours)
                </label>
                <Input
                  id="backup-interval"
                  type="number"
                  min="1"
                  max="8760"
                  className="max-w-32"
                  value={draft.intervalHours}
                  onChange={(event) => setDraft({ ...draft, intervalHours: Number(event.target.value) || 0 })}
                />
              </div>
              <div className="space-y-2">
                <label htmlFor="backup-retention" className="text-sm font-medium">
                  Keep backups
                </label>
                <Input
                  id="backup-retention"
                  type="number"
                  min="1"
                  max="200"
                  className="max-w-32"
                  value={draft.retentionCount}
                  onChange={(event) => setDraft({ ...draft, retentionCount: Number(event.target.value) || 0 })}
                />
                <p className="text-xs text-muted-foreground">Rollback copies are never removed automatically.</p>
              </div>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button type="submit" size="sm" disabled={!dirty || busy !== null}>
                {busy === 'config' ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <Save />}
                Save schedule
              </Button>
            </div>
          </CardContent>
        </Card>
      </form>

      <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle className="flex items-center gap-2">
            <HardDriveDownload className="size-4 text-muted-foreground" />
            Backups
            <Badge variant="outline">{backups.length}</Badge>
          </CardTitle>
          <CardDescription>
            Each backup stores a consistent PostgreSQL dump with a checksummed manifest. Restoring validates the archive,
            keeps a rollback copy, and recovers the database and saved application settings.
          </CardDescription>
        </CardHeader>
        <CardContent className="gap-4">
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={busy !== null} onClick={() => setDialog({ kind: 'create' })}>
              {busy === 'create' ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <DatabaseBackup />}
              Create backup now
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={busy !== null}
              onClick={() => fileInput.current?.click()}
            >
              {busy === 'import' ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <Upload />}
              Import archive
            </Button>
            <input
              ref={fileInput}
              type="file"
              accept=".tar,.zip,.gz,application/x-tar,application/zip,application/gzip"
              className="hidden"
              aria-label="Import a backup archive"
              onChange={(event) => {
                const file = event.target.files?.[0]
                if (file) void importBackupFile(file)
              }}
            />
            <Button type="button" size="sm" variant="outline" disabled={busy !== null} onClick={() => void refresh()}>
              <RefreshCw />
              Refresh
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            Uploads up to {formatBytes(maxUploadBytes)} are validated as tar, zip, or gzip archives: entries, checksums,
            manifest version, and dump format are all checked before anything is stored.
          </p>

          {backups.length === 0 && <EmptyState>No backups yet. Create one before changing settings or restoring.</EmptyState>}
          <ul className="space-y-2">
            {backups.map((backup) => (
              <li key={backup.id} className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-3">
                <div className="min-w-48 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="text-xs">{backup.id}</code>
                    <Badge variant="outline">{originLabels[backup.origin]}</Badge>
                    {backup.problem ? (
                      <Badge variant="destructive">Damaged</Badge>
                    ) : (
                      <Badge variant="outline" className="border-emerald-500/30 text-emerald-400">
                        {backup.verified ? 'Verified' : 'Stored'}
                      </Badge>
                    )}
                    {backup.rollbackFor && <Badge variant="outline">for {backup.rollbackFor}</Badge>}
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {new Date(backup.createdAt).toLocaleString()} · {formatBytes(backup.bytes)} · database schema{' '}
                    {backup.schemaVersion}
                    {backup.serverVersion ? ` · PostgreSQL ${backup.serverVersion}` : ''}
                  </p>
                  {backup.problem && <p className="mt-1 text-xs text-destructive">{backup.problem}</p>}
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button asChild size="sm" variant="outline">
                    <a href={operationsApi.downloadUrl(backup.id)} download={`${backup.id}.tar`}>
                      <Download />
                      Download
                    </a>
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy !== null || !canRestore}
                    title={canRestore ? undefined : 'Restoring needs full administration: backups.manage and users.manage'}
                    onClick={() => void openRestore(backup)}
                  >
                    <RotateCcw />
                    Restore
                  </Button>
                  <Button
                    size="sm"
                    variant="destructive"
                    disabled={busy !== null}
                    onClick={() => setDialog({ kind: 'delete', backup })}
                  >
                    <Trash2 />
                    Delete
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      <DialogShell
        active={dialog?.kind === 'create'}
        title="Create a backup now"
        description="The backup is stored in the managed backup directory on the server."
        onClose={() => setDialog(null)}
      >
        <div className="space-y-4 text-sm">
          <p>The archive contains:</p>
          <ul className="list-disc space-y-1 pl-5 text-muted-foreground">
            <li>PostgreSQL database: catalogs, configuration, permissions, task history, and stored credentials</li>
            <li>Configuration files from the data directory</li>
          </ul>
          <p>It never contains:</p>
          <ul className="list-disc space-y-1 pl-5 text-muted-foreground">
            <li>Media files and library roots</li>
            <li>Downloads, posters, and probe caches</li>
          </ul>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => setDialog(null)}>
              Cancel
            </Button>
            <Button type="button" size="sm" disabled={busy !== null} onClick={() => void createBackup()}>
              {busy === 'create' ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <DatabaseBackup />}
              Create backup
            </Button>
          </div>
        </div>
      </DialogShell>

      <DialogShell
        active={dialog?.kind === 'delete'}
        title="Delete this backup?"
        description={dialog?.kind === 'delete' ? `Backup ${dialog.backup.id} is removed from the server.` : ''}
        onClose={() => setDialog(null)}
      >
        {dialog?.kind === 'delete' && (
          <div className="space-y-4 text-sm">
            <p>
              <code>{dialog.backup.id}</code> was created {new Date(dialog.backup.createdAt).toLocaleString()} and holds{' '}
              {formatBytes(dialog.backup.bytes)}. Deleting it cannot be undone.
            </p>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" size="sm" onClick={() => setDialog(null)}>
                Keep backup
              </Button>
              <Button type="button" variant="destructive" size="sm" disabled={busy !== null} onClick={() => void deleteBackup(dialog.backup)}>
                {busy === 'delete' ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <Trash2 />}
                Delete {dialog.backup.id}
              </Button>
            </div>
          </div>
        )}
      </DialogShell>

      <DialogShell
        active={dialog?.kind === 'restore'}
        title="Restore the database"
        description={dialog?.kind === 'restore' ? `Replaces the live database with backup ${dialog.backup.id}.` : ''}
        onClose={() => setDialog(null)}
      >
        {dialog?.kind === 'restore' && (
          <div className="space-y-4 text-sm">
            {busy === 'plan' && <LoadingNote>Validating the backup…</LoadingNote>}
            {plan && (
              <>
                <ul className="space-y-1">
                  <PlanRow ok={plan.checksumVerified} label="Archive checksums verified" />
                  <PlanRow ok={plan.schemaCompatible} label={`Schema version ${plan.backup.schemaVersion} fits this server (${plan.currentSchemaVersion})`} />
                  <PlanRow ok={plan.willCreateRollbackBackup} label="A rollback copy of the current database is created first" />
                  <PlanRow ok={plan.liveRestoreEnabled} label="Live restores are enabled on this server" />
                  <PlanRow ok={plan.quiesceConfigured} label="Automation can be paused during the restore" />
                  <PlanRow
                    ok={plan.requiresRestart}
                    label={
                      plan.autoRestart
                        ? 'The server restarts and reconnects automatically afterwards'
                        : 'A server restart is required afterwards'
                    }
                    warn={!plan.autoRestart}
                  />
                </ul>
                {plan.warnings && plan.warnings.length > 0 && (
                  <div className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-200">
                    {plan.warnings.map((warning) => (
                      <p key={warning}>{warning}</p>
                    ))}
                  </div>
                )}
                <div className="rounded-md border border-border p-3 text-xs text-muted-foreground">
                  <p className="font-medium text-foreground">Included in this archive</p>
                  <ul className="mt-1 list-disc space-y-1 pl-4">
                    {plan.backup.included.map((entry) => (
                      <li key={entry}>{entry}</li>
                    ))}
                  </ul>
                  <p className="mt-2 font-medium text-foreground">Not included</p>
                  <ul className="mt-1 list-disc space-y-1 pl-4">
                    {plan.backup.excluded.map((entry) => (
                      <li key={entry}>{entry}</li>
                    ))}
                  </ul>
                </div>
                {result ? (
                  <div className="space-y-2 rounded-md border border-emerald-500/30 bg-emerald-500/5 p-3">
                    <p className="font-medium text-emerald-300">Restore finished in {result.durationSeconds.toFixed(1)}s</p>
                    {result.rollbackBackupId && (
                      <p>
                        Rollback copy: <code>{result.rollbackBackupId}</code>
                      </p>
                    )}
                    {(result.notes ?? []).map((note) => (
                      <p key={note} className="text-muted-foreground">
                        {note}
                      </p>
                    ))}
                    {result.autoRestart && reconnecting && (
                      <p className="flex items-center gap-2 text-amber-200">
                        <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" />
                        Waiting for the restarted server to reconnect…
                      </p>
                    )}
                  </div>
                ) : !canRestore ? (
                  <p className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-amber-200">
                    <ShieldAlert className="mt-0.5 size-4 shrink-0" />
                    Restoring needs full administration (backups.manage and users.manage) in a signed-in browser
                    session, because it replaces accounts, sessions, and stored credentials.
                  </p>
                ) : (
                  <>
                    <p className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-destructive">
                      <ShieldAlert className="mt-0.5 size-4 shrink-0" />
                      {plan.autoRestart
                        ? 'This replaces the live database. The server restarts and reconnects automatically; sessions that were created after this backup are signed out.'
                        : 'This replaces the live database. Every service must be restarted afterwards.'}
                    </p>
                    <p className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-200">
                      Restore only backups from a server you trust. This replaces accounts, sessions, API tokens,
                      and provider credentials, and executes the database commands in the archive.
                    </p>
                    <p className="text-xs text-muted-foreground">
                      Application settings are restored from the database. Extra configuration files in the archive
                      are available for manual recovery and are not written over files on this server.
                    </p>
                    <div className="space-y-2">
                      <label htmlFor="restore-confirm" className="text-sm font-medium">
                        Type <code>{dialog.backup.id}</code> to confirm
                      </label>
                      <Input
                        id="restore-confirm"
                        value={confirm}
                        autoComplete="off"
                        onChange={(event) => setConfirm(event.target.value)}
                      />
                    </div>
                    <div className="flex justify-end gap-2">
                      <Button type="button" variant="outline" size="sm" onClick={() => setDialog(null)}>
                        Cancel
                      </Button>
                      <Button
                        type="button"
                        variant="destructive"
                        size="sm"
                        disabled={
                          busy !== null ||
                          confirm !== dialog.backup.id ||
                          !plan.checksumVerified ||
                          !plan.schemaCompatible ||
                          !plan.liveRestoreEnabled ||
                          !plan.quiesceConfigured
                        }
                        onClick={() => void restoreBackup(dialog.backup)}
                      >
                        {busy === 'restore' ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <RotateCcw />}
                        Restore database
                      </Button>
                    </div>
                  </>
                )}
              </>
            )}
          </div>
        )}
      </DialogShell>
    </div>
  )
}

function PlanRow({ ok, label, warn }: { ok: boolean; label: string; warn?: boolean }) {
  return (
    <li className="flex items-center gap-2">
      <span
        aria-hidden="true"
        className={
          ok
            ? warn
              ? 'size-2 rounded-full bg-amber-400'
              : 'size-2 rounded-full bg-emerald-400'
            : 'size-2 rounded-full bg-destructive'
        }
      />
      <span className={ok ? '' : 'text-destructive'}>{label}</span>
    </li>
  )
}
