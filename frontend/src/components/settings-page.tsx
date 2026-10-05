import { useEffect, useState, type FormEvent } from 'react'
import { Check, Database, Eye, EyeOff, Folder, HardDrive, KeyRound, LoaderCircle, Radio, RefreshCw, Save, Search, ShieldAlert, ShieldCheck } from 'lucide-react'
import { BackendStatus } from '@/components/backend-status'
import { MovieConfiguration } from '@/components/movie-configuration'
import { TVConfiguration } from '@/components/tv-configuration'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { api, errorMessage, type Settings, type SettingsUpdate, type SourceTestResult } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'

type Draft = SettingsUpdate & { fallbacks: string }

function toDraft(settings: Settings): Draft {
  return {
    indexer: { url: settings.indexer.url, apiKey: '' },
    usenet: { ...settings.usenet, password: '' },
    fallbacks: settings.usenet.fallbackHosts.join(', '),
  }
}

function SecretInput({ id, label, configured, value, onChange }: {
  id: string; label: string; configured: boolean; value: string; onChange: (value: string) => void
}) {
  const [visible, setVisible] = useState(false)
  return (
    <div className="space-y-2">
      <label htmlFor={id} className="text-sm font-medium">{label}</label>
      <div className="relative">
        <Input id={id} type={visible ? 'text' : 'password'} value={value} onChange={event => onChange(event.target.value)}
          autoComplete="new-password" placeholder={configured ? 'Saved credential (leave blank to keep)' : `Enter your ${label.toLowerCase()}`} className="pr-11" aria-describedby={`${id}-hint`} />
        <Button type="button" variant="ghost" size="icon-sm" className="absolute top-0.5 right-1" aria-label={`${visible ? 'Hide' : 'Show'} ${label.toLowerCase()}`} aria-pressed={visible} onClick={() => setVisible(!visible)}>
          {visible ? <EyeOff /> : <Eye />}
        </Button>
      </div>
      <p id={`${id}-hint`} className="text-xs text-muted-foreground">{configured ? 'A credential is saved. Enter a new value only to replace it.' : 'Stored on your server and never returned to the browser.'}</p>
    </div>
  )
}

function TestResult({ result }: { result?: SourceTestResult }) {
  if (!result) return null
  return <p role={result.ok ? 'status' : 'alert'} className={`rounded-lg border p-3 text-sm ${result.ok ? 'border-emerald-500/20 text-emerald-500' : 'border-destructive/30 text-destructive'}`}>
    {result.ok ? <><Check className="mr-2 inline size-4" />Connection verified</> : result.error || 'Connection failed. Check your configuration.'}
  </p>
}

export function SettingsPage({ section }: { section: 'connections' | 'storage' | 'system' }) {
  const { can } = useAuth()
  const canWrite = can(accessPermissions.settingsWrite)
  const canRead = can(accessPermissions.settingsRead)
  const [saved, setSaved] = useState<Settings | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState<'save' | 'test' | null>(null)
  const [tests, setTests] = useState<{ indexer: SourceTestResult; usenet: SourceTestResult } | null>(null)
  const [loading, setLoading] = useState(true)
  const dirty = saved !== null && draft !== null && JSON.stringify(draft) !== JSON.stringify(toDraft(saved))

  useEffect(() => {
    if (!canRead) return
    const controller = new AbortController()
    api.getSettings(controller.signal).then(settings => { setSaved(settings); setDraft(toDraft(settings)) })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [canRead])

  useEffect(() => {
    if (!dirty) return
    function preventLoss(event: BeforeUnloadEvent) { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', preventLoss)
    return () => window.removeEventListener('beforeunload', preventLoss)
  }, [dirty])

  function change(next: Draft) { setDraft(next); setNotice(''); setTests(null); setError('') }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (!draft || !canWrite) return
    setBusy('save'); setError(''); setNotice('')
    try {
      const { fallbacks, indexer, usenet } = draft
      const settings = await api.saveSettings({
        indexer,
        usenet: { host: usenet.host, port: usenet.port, username: usenet.username, password: usenet.password, connections: usenet.connections,
          fallbackHosts: fallbacks.split(',').map(host => host.trim()).filter(Boolean) },
      })
      setSaved(settings); setDraft(toDraft(settings)); setTests(null)
      setNotice('Settings saved. New searches and downloads use this configuration.')
      window.dispatchEvent(new Event('settings-saved'))
    } catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(null) }
  }

  async function test() {
    if (!canWrite) return
    setBusy('test'); setTests(null); setError(''); setNotice('')
    try { setTests(await api.testSources()) }
    catch (cause) { setError(errorMessage(cause)) }
    finally { setBusy(null) }
  }

  // Without settings.read nothing on this page can be shown or fetched.
  if (!canRead) {
    return (
      <div className="space-y-7">
        <header className="space-y-2">
          <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
        </header>
        <p role="status" className="flex items-start gap-2 rounded-lg border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
          <ShieldAlert className="mt-0.5 size-4 shrink-0 text-primary" aria-hidden="true" />
          Server and connection settings require the settings read permission. Ask an administrator for access.
        </p>
      </div>
    )
  }

  return (
    <div className="space-y-7">
      <header className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">{section === 'storage' ? 'Storage & Paths' : section === 'system' ? 'System' : 'Connections'}</h1>
        <p className="text-sm text-muted-foreground">{section === 'storage' ? 'Download directories and persistent storage for this server.' : section === 'system' ? 'Server health and configuration.' : 'Configure and test your indexer and Usenet provider.'}</p>
      </header>
      {error && <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">
        <span>{error}</span>{!saved && <Button variant="outline" size="sm" onClick={() => window.location.reload()}>Retry</Button>}
      </div>}
      {notice && section === 'connections' && <p role="status" className="rounded-lg border border-emerald-500/20 bg-emerald-500/5 p-4 text-sm text-emerald-500"><Check className="mr-2 inline size-4" />{notice}</p>}
      {loading && <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />Loading settings…</p>}
      {saved && draft && <form onSubmit={save} className="space-y-6">
        <fieldset disabled={!canWrite} className="contents">
          {section === 'connections' && <div className="space-y-5">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="flex items-center gap-2 text-xs text-muted-foreground"><ShieldCheck className="size-4" />Credentials stay on your server.</p>
              {canWrite && <Button type="button" variant="outline" size="sm" onClick={test} disabled={Boolean(busy) || dirty} title={dirty ? 'Save or discard changes before testing.' : undefined}>
                {busy === 'test' ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}Test connections
              </Button>}
            </div>
            <div className="grid items-start gap-5 xl:grid-cols-2">
              <Card className="shadow-none">
                <CardHeader className="border-b border-border">
                  <CardTitle className="flex items-center justify-between gap-2"><span className="flex items-center gap-2"><Search className="size-4 text-muted-foreground" />NZBGeek</span><Badge variant="outline">{saved.indexer.apiKeyConfigured ? 'Configured' : 'Setup needed'}</Badge></CardTitle>
                  <CardDescription>Your indexer for finding movie releases.</CardDescription>
                </CardHeader>
                <CardContent className="gap-5">
                  <div className="space-y-2"><label htmlFor="indexer-url" className="text-sm font-medium">API URL</label><Input id="indexer-url" type="url" required value={draft.indexer.url} onChange={event => change({ ...draft, indexer: { ...draft.indexer, url: event.target.value } })} /></div>
                  <SecretInput id="api-key" label="API key" configured={saved.indexer.apiKeyConfigured} value={draft.indexer.apiKey || ''} onChange={apiKey => change({ ...draft, indexer: { ...draft.indexer, apiKey } })} />
                  <TestResult result={tests?.indexer} />
                </CardContent>
              </Card>
              <Card className="shadow-none">
                <CardHeader className="border-b border-border">
                  <CardTitle className="flex items-center justify-between gap-2"><span className="flex items-center gap-2"><Radio className="size-4 text-muted-foreground" />Usenet provider</span><Badge variant="outline">{saved.usenet.username && saved.usenet.passwordConfigured ? 'Configured' : 'Setup needed'}</Badge></CardTitle>
                  <CardDescription>Connect to Frugal Usenet or another provider over TLS.</CardDescription>
                </CardHeader>
                <CardContent className="gap-5">
                  <div className="grid grid-cols-[minmax(0,1fr)_6rem] gap-4">
                    <div className="space-y-2"><label htmlFor="usenet-host" className="text-sm font-medium">Server hostname</label><Input id="usenet-host" required value={draft.usenet.host} onChange={event => change({ ...draft, usenet: { ...draft.usenet, host: event.target.value } })} /></div>
                    <div className="space-y-2"><label htmlFor="usenet-port" className="text-sm font-medium">TLS port</label><Input id="usenet-port" type="number" min="1" max="65535" required value={draft.usenet.port} onChange={event => change({ ...draft, usenet: { ...draft.usenet, port: Number(event.target.value) } })} /></div>
                  </div>
                  <div className="space-y-2"><label htmlFor="usenet-username" className="text-sm font-medium">Username</label><Input id="usenet-username" autoComplete="off" value={draft.usenet.username} onChange={event => change({ ...draft, usenet: { ...draft.usenet, username: event.target.value } })} /></div>
                  <SecretInput id="usenet-password" label="Password" configured={saved.usenet.passwordConfigured} value={draft.usenet.password || ''} onChange={password => change({ ...draft, usenet: { ...draft.usenet, password } })} />
                  <div className="space-y-2"><label htmlFor="usenet-connections" className="text-sm font-medium">Connections</label><Input id="usenet-connections" type="number" min="1" max="32" required value={draft.usenet.connections} className="max-w-28" onChange={event => change({ ...draft, usenet: { ...draft.usenet, connections: Number(event.target.value) } })} /><p className="text-xs text-muted-foreground">Use the connection limit included with your provider plan.</p></div>
                  <div className="space-y-2"><label htmlFor="usenet-fallbacks" className="text-sm font-medium">Fallback servers</label><Input id="usenet-fallbacks" value={draft.fallbacks} placeholder="news.frugalusenet.com" onChange={event => change({ ...draft, fallbacks: event.target.value })} /><p className="text-xs text-muted-foreground">Optional, comma separated. Uses the same credentials and TLS port.</p></div>
                  <TestResult result={tests?.usenet} />
                </CardContent>
              </Card>
            </div>
          </div>}
          {section === 'storage' && <div className="space-y-5">
            <Card className="max-w-3xl shadow-none">
              <CardHeader className="border-b border-border"><CardTitle className="flex items-center gap-2"><Folder className="size-4 text-muted-foreground" />Download storage</CardTitle><CardDescription>Where Constellarr assembles, verifies, and extracts your downloads.</CardDescription></CardHeader>
              <CardContent className="gap-5">
                <div className="space-y-2"><label htmlFor="storage-directory" className="text-sm font-medium">Data directory</label><Input id="storage-directory" readOnly value={saved.storage.directory} className="font-mono text-xs" /><p className="text-xs leading-relaxed text-muted-foreground">Set by your server configuration. In Docker, mount your chosen folder at /data; existing downloads remain in that folder.</p></div>
                <div className="flex gap-3 rounded-lg border border-border p-4"><HardDrive className="mt-0.5 size-4 shrink-0 text-muted-foreground" /><div className="space-y-1"><p className="text-sm font-medium">Resume without starting over</p><p className="text-xs leading-relaxed text-muted-foreground">Article cache and completed files stay in your data directory. Keep this volume when updating or restarting Constellarr.</p></div></div>
              </CardContent>
            </Card>
          </div>}
          {section === 'system' && <div className="grid items-start gap-5 xl:grid-cols-2">
            <BackendStatus />
            <Card className="shadow-none"><CardHeader className="border-b border-border"><CardTitle className="flex items-center gap-2"><Database className="size-4 text-muted-foreground" />Configuration</CardTitle><CardDescription>One place for your media setup.</CardDescription></CardHeader><CardContent className="gap-4 text-sm"><p className="text-muted-foreground">Connections are saved in PostgreSQL and restored on restart. Server environment values provide the initial defaults.</p><p className="flex items-center gap-2 text-xs text-muted-foreground"><KeyRound className="size-4 shrink-0" />Saved keys and passwords are never sent back to this interface.</p></CardContent></Card>
          </div>}
        {section === 'connections' && canWrite && <footer className="sticky bottom-0 mt-6 flex flex-wrap items-center justify-between gap-3 border-t border-border bg-background/95 py-4 backdrop-blur-sm">
          <p className="text-xs text-muted-foreground" role="status">{dirty ? 'You have unsaved changes.' : 'All changes saved.'}</p>
          <div className="flex gap-2"><Button type="button" variant="outline" size="sm" disabled={!dirty || Boolean(busy)} onClick={() => { setDraft(toDraft(saved)); setError(''); setNotice(''); setTests(null) }}>Discard changes</Button><Button type="submit" size="sm" disabled={!dirty || Boolean(busy)}>{busy === 'save' ? <LoaderCircle className="animate-spin" /> : <Save />}Save changes</Button></div>
        </footer>}
        {section === 'connections' && !canWrite && <p className="mt-6 border-t border-border pt-4 text-xs text-muted-foreground">Your role can view these settings but not change them.</p>}
        </fieldset>
      </form>}
      <div hidden={section === 'system' || !canRead} inert={section === 'system' || !canRead}>
        <MovieConfiguration section={section === 'storage' ? 'storage' : 'connections'} />
      </div>
      <div hidden={section !== 'storage' || !canRead} inert={section !== 'storage' || !canRead}>
        <TVConfiguration />
      </div>
    </div>
  )
}
