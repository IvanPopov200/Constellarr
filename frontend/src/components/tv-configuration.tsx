import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Folder, HardDrive, Info, LoaderCircle, Plus, Save, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Checkbox, ErrorNote, Notice, Select } from '@/components/tv-ui'
import { errorMessage } from '@/lib/api'
import { templateTokens } from '@/components/tv-shared'
import { tvApi, type TvConfig } from '@/lib/tv-api'

const importModes = [
  { value: 'copy', label: 'Copy' },
  { value: 'hardlink', label: 'Hardlink' },
  { value: 'move', label: 'Move' },
]

function optionsWith(current: string, options: { value: string; label: string }[]) {
  if (!current || options.some((option) => option.value === current)) return options
  return [...options, { value: current, label: current }]
}

export function TVConfiguration() {
  const [saved, setSaved] = useState<TvConfig | null>(null)
  const [draft, setDraft] = useState<TvConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    tvApi
      .config(controller.signal)
      .then((config) => {
        if (controller.signal.aborted) return
        setSaved(config)
        setDraft(config)
        setError('')
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [reloadKey])

  const dirty = saved !== null && draft !== null && JSON.stringify(draft) !== JSON.stringify(saved)
  const rootsValid = draft === null || draft.rootFolders.every((root) => root.path.trim().length > 0)

  function change(next: TvConfig) {
    setDraft(next)
    setError('')
    setNotice('')
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    event.stopPropagation()
    if (!draft) return
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const config = await tvApi.saveConfig(draft)
      setSaved(config)
      setDraft(config)
      setNotice('TV settings saved. New imports and automation use this configuration.')
      window.dispatchEvent(new Event('tv-changed'))
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  if (loading && !draft) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" />
        Loading TV settings…
      </p>
    )
  }

  if (!saved || !draft) {
    return (
      <div
        role="alert"
        className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
      >
        <span>{error || 'Could not load TV settings.'}</span>
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            setLoading(true)
            setReloadKey((current) => current + 1)
          }}
        >
          Retry
        </Button>
      </div>
    )
  }

  return (
    <form onSubmit={save} className="space-y-5">
      {error && <ErrorNote>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}

      <div className="grid items-start gap-5 xl:grid-cols-2">
        <Card className="shadow-none">
          <CardHeader className="border-b border-border">
            <CardTitle className="flex items-center gap-2">
              <Folder className="size-4 text-muted-foreground" />
              TV library folders
            </CardTitle>
            <CardDescription>Where imported series and episodes are organized on your server.</CardDescription>
          </CardHeader>
          <CardContent className="gap-5">
            <div className="space-y-3">
              <span className="text-sm font-medium">Root folders</span>
              {draft.rootFolders.length === 0 && (
                <p className="rounded-md border border-dashed border-border px-3 py-4 text-sm text-muted-foreground">
                  No TV root folders yet. Add the folder that holds your shows.
                </p>
              )}
              <ul className="space-y-2">
                {draft.rootFolders.map((root, index) => (
                  <li key={`${root.id || 'new'}-${index}`} className="flex items-center gap-2">
                    <Input
                      value={root.path}
                      aria-label={`TV root folder ${index + 1} path`}
                      placeholder="/media/tv"
                      className="font-mono text-xs"
                      onChange={(event) =>
                        change({
                          ...draft,
                          rootFolders: draft.rootFolders.map((item, itemIndex) =>
                            itemIndex === index ? { ...item, path: event.target.value } : item,
                          ),
                        })
                      }
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Remove root folder ${root.path || index + 1}`}
                      onClick={() =>
                        change({
                          ...draft,
                          rootFolders: draft.rootFolders.filter((_, itemIndex) => itemIndex !== index),
                        })
                      }
                    >
                      <Trash2 />
                    </Button>
                  </li>
                ))}
              </ul>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => change({ ...draft, rootFolders: [...draft.rootFolders, { id: '', path: '' }] })}
              >
                <Plus />
                Add TV root folder
              </Button>
              <p className="text-xs text-muted-foreground">
                New folders are registered when you save. Existing files are never moved by saving settings.
              </p>
            </div>

            <div className="space-y-2">
              <label htmlFor="tv-folder-template" className="text-sm font-medium">
                Series folder template
              </label>
              <Input
                id="tv-folder-template"
                value={draft.folderTemplate}
                placeholder="{title} ({year})"
                onChange={(event) => change({ ...draft, folderTemplate: event.target.value })}
              />
              <p className="text-xs text-muted-foreground">Relative path. Include at least one token.</p>
            </div>
            <div className="space-y-2">
              <label htmlFor="tv-file-template" className="text-sm font-medium">
                Episode file template
              </label>
              <Input
                id="tv-file-template"
                value={draft.fileTemplate}
                placeholder="{title} - S{season}E{episode} - {episodeCode}"
                onChange={(event) => change({ ...draft, fileTemplate: event.target.value })}
              />
              <p className="text-xs text-muted-foreground">
                Imports keep the source extension. Include {'{episodeCode}'}, {'{episode}'}, or {'{original}'} so
                episodes never collide inside the season folder.
              </p>
            </div>

            <div className="space-y-2 rounded-md border border-border p-3">
              <p className="flex items-center gap-2 text-xs font-medium">
                <Info className="size-3.5 text-muted-foreground" />
                Naming tokens
              </p>
              <ul className="grid gap-1 text-xs text-muted-foreground sm:grid-cols-2">
                {templateTokens.map((token) => (
                  <li key={token.token}>
                    <code className="text-foreground">{token.token}</code> — {token.label}
                  </li>
                ))}
              </ul>
            </div>

            <div className="space-y-2">
              <label htmlFor="tv-import-mode" className="text-sm font-medium">
                Import mode
              </label>
              <Select
                id="tv-import-mode"
                value={draft.importMode}
                onChange={(event) => change({ ...draft, importMode: event.target.value })}
              >
                {optionsWith(draft.importMode, importModes).map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </Select>
              <p className="text-xs text-muted-foreground">
                Hardlink keeps seeding data usable; move relocates the file.
              </p>
            </div>
            <Checkbox
              id="tv-write-nfo"
              label="Write NFO sidecars"
              description="Save series and episode details next to the files for other media tools."
              checked={draft.writeNFO}
              onChange={(writeNFO) => change({ ...draft, writeNFO })}
            />
          </CardContent>
        </Card>

        <Card className="shadow-none">
          <CardHeader className="border-b border-border">
            <CardTitle className="flex items-center gap-2">
              <HardDrive className="size-4 text-muted-foreground" />
              TV automation
            </CardTitle>
            <CardDescription>Scheduled searches and how failed grabs are handled.</CardDescription>
          </CardHeader>
          <CardContent className="gap-5">
            <div className="space-y-2">
              <label htmlFor="tv-poll-minutes" className="text-sm font-medium">
                RSS check interval (minutes)
              </label>
              <Input
                id="tv-poll-minutes"
                type="number"
                min="1"
                className="max-w-32"
                value={draft.pollMinutes}
                onChange={(event) => change({ ...draft, pollMinutes: Number(event.target.value) || 0 })}
              />
              <p className="text-xs text-muted-foreground">How often indexer feeds are checked for wanted episodes.</p>
            </div>
            <div className="space-y-2">
              <label htmlFor="tv-search-hours" className="text-sm font-medium">
                Missing search interval (hours)
              </label>
              <Input
                id="tv-search-hours"
                type="number"
                min="1"
                className="max-w-32"
                value={draft.searchHours}
                onChange={(event) => change({ ...draft, searchHours: Number(event.target.value) || 0 })}
              />
              <p className="text-xs text-muted-foreground">
                How often missing monitored episodes are searched automatically.
              </p>
            </div>
            <Checkbox
              id="tv-retry-failed"
              label="Retry failed downloads"
              description="Failed releases are blocked, and alternate releases are tried on the next search."
              checked={draft.retryFailed}
              onChange={(retryFailed) => change({ ...draft, retryFailed })}
            />
            <p className="text-xs leading-relaxed text-muted-foreground">
              Metadata, indexer, Usenet, and Jellyfin connections are shared with movies and are configured under
              Connections. Quality profiles are shared under Movies → Profiles.
            </p>
          </CardContent>
        </Card>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4">
        <p className="text-xs text-muted-foreground" role="status">
          {dirty ? 'You have unsaved TV settings.' : 'TV settings are saved.'}
        </p>
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={!dirty || busy}
            onClick={() => {
              setDraft(saved)
              setError('')
              setNotice('')
            }}
          >
            Discard
          </Button>
          <Button type="submit" size="sm" disabled={!dirty || busy || !rootsValid}>
            {busy ? (
              <LoaderCircle className="animate-spin motion-reduce:animate-none" />
            ) : (
              <Save />
            )}
            Save TV settings
          </Button>
        </div>
      </div>
    </form>
  )
}
