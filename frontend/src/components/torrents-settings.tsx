import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { ActivityIcon, PlusIcon, Trash2Icon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import {
  torrentsApi,
  type TorrentSettings,
  type TorrentSource,
  type TorrentSourceInput,
} from '@/lib/torrents-api'
import { parseCategories } from '@/components/torrents-shared'

type Props = {
  sources: TorrentSource[]
  canWrite: boolean
  onSourcesChanged: () => void
}

const emptySource: TorrentSourceInput = { name: '', url: '', apiKey: '', categories: [], enabled: true }

export function TorrentSettingsCard({ sources, canWrite, onSourcesChanged }: Props) {
  const [settings, setSettings] = useState<TorrentSettings | null>(null)
  const [form, setForm] = useState<TorrentSettings | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const [sourceDraft, setSourceDraft] = useState<TorrentSourceInput>(emptySource)
  const [sourceCategories, setSourceCategories] = useState('')
  const [editingId, setEditingId] = useState<string | null>(null)
  const [sourceError, setSourceError] = useState<string | null>(null)
  const [sourceCaps, setSourceCaps] = useState<Record<string, string[]>>({})

  useEffect(() => {
    let stopped = false
    const load = async () => {
      try {
        const loaded = await torrentsApi.settings()
        if (stopped) return
        setSettings(loaded)
        setForm(loaded)
        setError(null)
      } catch (cause) {
        if (!stopped) setError(errorMessage(cause))
      }
    }
    void load()
    const timer = window.setInterval(() => void load(), 10_000)
    return () => {
      stopped = true
      window.clearInterval(timer)
    }
  }, [])

  const save = async (event: FormEvent) => {
    event.preventDefault()
    if (!form) return
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const saved = await torrentsApi.saveSettings({
        listenPort: form.listenPort,
        dhtEnabled: form.dhtEnabled,
        pexEnabled: form.pexEnabled,
        maxActiveJobs: form.maxActiveJobs,
        downloadLimitKBps: form.downloadLimitKBps,
        uploadLimitKBps: form.uploadLimitKBps,
        seedRatioLimit: form.seedRatioLimit,
        seedTimeLimitMinutes: form.seedTimeLimitMinutes,
      })
      setSettings(saved)
      setForm(saved)
      setNotice(saved.restartRequired ? 'Saved. Restart the server to apply the port or peer discovery change.' : 'Saved.')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  const submitSource = async (event: FormEvent) => {
    event.preventDefault()
    const categories = parseCategories(sourceCategories)
    if (!sourceDraft.name.trim() || !sourceDraft.url.trim()) {
      setSourceError('A name and a Torznab URL are required.')
      return
    }
    if (categories === null) {
      setSourceError('Categories must be whole numbers separated by commas.')
      return
    }
    setBusy(true)
    setSourceError(null)
    try {
      const input = { ...sourceDraft, name: sourceDraft.name.trim(), url: sourceDraft.url.trim(), categories }
      if (editingId) {
        await torrentsApi.updateSource(editingId, input)
      } else {
        await torrentsApi.createSource(input)
      }
      setSourceDraft(emptySource)
      setSourceCategories('')
      setEditingId(null)
      onSourcesChanged()
    } catch (cause) {
      setSourceError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  const editSource = (source: TorrentSource) => {
    setEditingId(source.id)
    setSourceDraft({ name: source.name, url: source.url, apiKey: '', categories: source.categories, enabled: source.enabled })
    setSourceCategories(source.categories.join(', '))
    setSourceError(null)
  }

  const removeSource = async (source: TorrentSource) => {
    if (!window.confirm(`Remove the indexer "${source.name}"?`)) return
    try {
      await torrentsApi.deleteSource(source.id)
      onSourcesChanged()
    } catch (cause) {
      setSourceError(errorMessage(cause))
    }
  }

  const testSource = async (source: TorrentSource) => {
    setSourceError(null)
    try {
      const result = await torrentsApi.testSource(source.id)
      if (!result.ok) {
        setSourceError(result.error || 'The source did not respond to a capabilities request.')
        return
      }
      setSourceCaps((current) => ({ ...current, [source.id]: result.caps }))
      onSourcesChanged()
    } catch (cause) {
      setSourceError(errorMessage(cause))
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>Engine settings</CardTitle>
          <CardDescription>
            Built-in BitTorrent engine. Download directory: <span className="font-mono">{settings?.directory || '—'}</span>
          </CardDescription>
        </CardHeader>
        <CardContent>
          {form ? (
            <form className="flex flex-col gap-4" onSubmit={save}>
              <fieldset disabled={!canWrite} className="contents">
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <label className="flex flex-col gap-1 text-sm">
                  Listening port
                  <Input
                    type="number"
                    min={1}
                    max={65535}
                    value={form.listenPort}
                    onChange={(event) => setForm({ ...form, listenPort: Number(event.target.value) })}
                  />
                </label>
                <label className="flex flex-col gap-1 text-sm">
                  Maximum active torrents
                  <Input
                    type="number"
                    min={1}
                    max={20}
                    value={form.maxActiveJobs}
                    onChange={(event) => setForm({ ...form, maxActiveJobs: Number(event.target.value) })}
                  />
                </label>
                <label className="flex flex-col gap-1 text-sm">
                  Download limit (KB/s, 0 = unlimited)
                  <Input
                    type="number"
                    min={0}
                    value={form.downloadLimitKBps}
                    onChange={(event) => setForm({ ...form, downloadLimitKBps: Number(event.target.value) })}
                  />
                </label>
                <label className="flex flex-col gap-1 text-sm">
                  Upload limit (KB/s, 0 = unlimited)
                  <Input
                    type="number"
                    min={0}
                    value={form.uploadLimitKBps}
                    onChange={(event) => setForm({ ...form, uploadLimitKBps: Number(event.target.value) })}
                  />
                </label>
                <label className="flex flex-col gap-1 text-sm">
                  Default seed ratio (0 = stop after download)
                  <Input
                    type="number"
                    min={0}
                    step="0.1"
                    value={form.seedRatioLimit}
                    onChange={(event) => setForm({ ...form, seedRatioLimit: Number(event.target.value) })}
                  />
                </label>
                <label className="flex flex-col gap-1 text-sm">
                  Default seed time (minutes, 0 = no limit)
                  <Input
                    type="number"
                    min={0}
                    value={form.seedTimeLimitMinutes}
                    onChange={(event) => setForm({ ...form, seedTimeLimitMinutes: Number(event.target.value) })}
                  />
                </label>
              </div>
              <div className="flex flex-wrap gap-4 text-sm">
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={form.dhtEnabled}
                    onChange={(event) => setForm({ ...form, dhtEnabled: event.target.checked })}
                  />
                  Enable DHT
                </label>
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={form.pexEnabled}
                    onChange={(event) => setForm({ ...form, pexEnabled: event.target.checked })}
                  />
                  Enable peer exchange
                </label>
              </div>
              </fieldset>
              <div className="flex flex-wrap items-center gap-3">
                {canWrite ? (
                  <Button type="submit" disabled={busy}>
                    Save settings
                  </Button>
                ) : null}
                {settings ? (
                  <span className="flex items-center gap-2 text-xs text-muted-foreground">
                    <ActivityIcon aria-hidden="true" className="size-3.5" />
                    {settings.restartRequired
                      ? 'The listening port or peer discovery change applies after a restart.'
                      : 'Configuration is stored on the server.'}
                  </span>
                ) : null}
              </div>
            </form>
          ) : (
            <p className="text-sm text-muted-foreground">Loading settings…</p>
          )}
          {!canWrite ? (
            <p className="mt-3 text-xs text-muted-foreground">
              Your role can view these settings but not change them.
            </p>
          ) : null}
          {error ? (
            <p role="alert" className="mt-3 text-sm text-destructive">
              {error}
            </p>
          ) : null}
          {notice ? (
            <p role="status" className="mt-3 text-sm text-muted-foreground">
              {notice}
            </p>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Torznab sources</CardTitle>
          <CardDescription>
            Indexer URLs and API keys stay on the server; API keys are never returned after saving.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {sources.length === 0 ? (
            <p className="text-sm text-muted-foreground">No sources configured yet.</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {sources.map((source) => (
                <li key={source.id} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border px-3 py-2">
                  <div className="flex min-w-0 flex-col">
                    <span className="text-sm">{source.name}</span>
                    <span className="truncate text-xs text-muted-foreground">{source.url}</span>
                    <span className="text-xs text-muted-foreground">
                      Categories: {source.categories.length > 0 ? source.categories.join(', ') : 'all'}
                      {sourceCaps[source.id]?.length ? ` · Capabilities: ${sourceCaps[source.id].join(', ')}` : ''}
                    </span>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={source.enabled ? 'secondary' : 'outline'}>{source.enabled ? 'Enabled' : 'Disabled'}</Badge>
                    {source.apiKeyConfigured ? <Badge variant="outline">API key saved</Badge> : null}
                    {canWrite ? (
                      <>
                        <Button size="sm" variant="outline" onClick={() => void testSource(source)}>
                          Test
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => editSource(source)}>
                          Edit
                        </Button>
                        <Button size="sm" variant="destructive" onClick={() => void removeSource(source)}>
                          <Trash2Icon aria-hidden="true" />
                          Remove
                        </Button>
                      </>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>
          )}

          {canWrite ? (
          <form className="flex flex-col gap-3 rounded-lg border border-border px-3 py-3" onSubmit={submitSource}>
            <div className="flex flex-wrap items-end gap-2">
              <label className="flex min-w-40 flex-1 flex-col gap-1 text-sm">
                Name
                <Input
                  value={sourceDraft.name}
                  onChange={(event) => setSourceDraft({ ...sourceDraft, name: event.target.value })}
                  placeholder="Prowlarr"
                />
              </label>
              <label className="flex min-w-56 flex-1 flex-col gap-1 text-sm">
                Torznab URL
                <Input
                  value={sourceDraft.url}
                  onChange={(event) => setSourceDraft({ ...sourceDraft, url: event.target.value })}
                  placeholder="http://host:9696/1/api"
                />
              </label>
              <label className="flex min-w-40 flex-1 flex-col gap-1 text-sm">
                API key
                <Input
                  type="password"
                  value={sourceDraft.apiKey ?? ''}
                  onChange={(event) => setSourceDraft({ ...sourceDraft, apiKey: event.target.value })}
                  placeholder={editingId ? 'Leave blank to keep the saved key' : 'Indexer API key'}
                />
              </label>
              <label className="flex min-w-32 flex-col gap-1 text-sm">
                Categories
                <Input
                  value={sourceCategories}
                  onChange={(event) => setSourceCategories(event.target.value)}
                  placeholder="2000, 5000"
                />
              </label>
              <label className="flex items-center gap-2 pb-2 text-sm">
                <input
                  type="checkbox"
                  checked={sourceDraft.enabled}
                  onChange={(event) => setSourceDraft({ ...sourceDraft, enabled: event.target.checked })}
                />
                Enabled
              </label>
            </div>
            <div className="flex items-center gap-2">
              <Button type="submit" size="sm" disabled={busy}>
                <PlusIcon aria-hidden="true" />
                {editingId ? 'Save source' : 'Add source'}
              </Button>
              {editingId ? (
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    setEditingId(null)
                    setSourceDraft(emptySource)
                    setSourceCategories('')
                  }}
                >
                  Cancel
                </Button>
              ) : null}
            </div>
            {sourceError ? (
              <p role="alert" className="text-sm text-destructive">
                {sourceError}
              </p>
            ) : null}
          </form>
          ) : (
            <p className="text-xs text-muted-foreground">
              Your role can view indexers but not add or change them.
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
