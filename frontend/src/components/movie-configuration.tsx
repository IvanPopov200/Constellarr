import { useEffect, useState } from 'react'
import type { ComponentProps, FormEvent, ReactNode } from 'react'
import {
  Check,
  Eye,
  EyeOff,
  Folder,
  HardDrive,
  KeyRound,
  LoaderCircle,
  Plus,
  RefreshCw,
  Save,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { moviesApi, type ConfigTest, type MovieConfig } from '@/lib/movies-api'
import { cn } from 'cn'

type Draft = MovieConfig & { metadataAPIKey: string; jellyfinAPIKey: string }

const importModes = [
  { value: 'copy', label: 'Copy' },
  { value: 'hardlink', label: 'Hardlink' },
  { value: 'move', label: 'Move' },
]

const availabilityOptions = [
  { value: 'announced', label: 'Announced' },
  { value: 'released', label: 'Released' },
]

function toDraft(config: MovieConfig): Draft {
  return { ...config, metadataAPIKey: '', jellyfinAPIKey: '' }
}

function optionsWith(current: string, options: { value: string; label: string }[]) {
  if (!current || options.some((option) => option.value === current)) return options
  return [...options, { value: current, label: current }]
}

function Field({
  id,
  label,
  hint,
  children,
  className,
}: {
  id: string
  label: string
  hint?: string
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('space-y-2', className)}>
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs leading-relaxed text-muted-foreground">{hint}</p>}
    </div>
  )
}

function SecretInput({
  id,
  label,
  configured,
  value,
  hint,
  onChange,
}: {
  id: string
  label: string
  configured: boolean
  value: string
  hint: string
  onChange: (value: string) => void
}) {
  const [visible, setVisible] = useState(false)
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        <Badge variant="outline">{configured ? 'Configured' : 'Not configured'}</Badge>
      </div>
      <div className="relative">
        <Input
          id={id}
          type={visible ? 'text' : 'password'}
          value={value}
          autoComplete="new-password"
          placeholder={configured ? 'Saved key — leave blank to keep' : `Enter your ${label.toLowerCase()}`}
          className="pr-11"
          onChange={(event) => onChange(event.target.value)}
        />
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="absolute top-0.5 right-1"
          aria-label={`${visible ? 'Hide' : 'Show'} ${label.toLowerCase()}`}
          aria-pressed={visible}
          onClick={() => setVisible((current) => !current)}
        >
          {visible ? <EyeOff /> : <Eye />}
        </Button>
      </div>
      <p className="text-xs leading-relaxed text-muted-foreground">{hint}</p>
    </div>
  )
}

function TestResult({ label, result }: { label: string; result?: { ok: boolean; error?: string } }) {
  if (!result) return null
  return (
    <li
      role={result.ok ? 'status' : 'alert'}
      className={cn(
        'flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-sm',
        result.ok
          ? 'border-emerald-500/20 text-emerald-400'
          : 'border-destructive/30 text-destructive',
      )}
    >
      <span className="font-medium">{label}</span>
      <Badge variant={result.ok ? 'default' : 'destructive'}>{result.ok ? 'Connected' : 'Failed'}</Badge>
      {!result.ok && result.error && <span className="text-muted-foreground">{result.error}</span>}
    </li>
  )
}

function Select({
  className,
  children,
  ...props
}: ComponentProps<'select'>) {
  return (
    <select
      className={cn(
        'h-9 rounded-md border border-input bg-transparent px-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 dark:bg-input/30',
        className,
      )}
      {...props}
    >
      {children}
    </select>
  )
}

function Checkbox({
  id,
  label,
  description,
  checked,
  onChange,
}: {
  id: string
  label: string
  description?: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-start gap-2.5">
      <input
        id={id}
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
        className="mt-0.5 size-4 shrink-0 accent-primary"
      />
      <div className="space-y-0.5">
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
    </div>
  )
}

export function MovieConfiguration({ section }: { section: 'connections' | 'storage' }) {
  const { can } = useAuth()
  const canWrite = can(accessPermissions.settingsWrite)
  const canRead = can(accessPermissions.settingsRead)
  const [saved, setSaved] = useState<MovieConfig | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<'save' | 'test' | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [tests, setTests] = useState<ConfigTest | null>(null)

  useEffect(() => {
    if (!canRead) return
    const controller = new AbortController()
    moviesApi
      .config(controller.signal)
      .then((config) => {
        setSaved(config)
        setDraft(toDraft(config))
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [canRead])

  const dirty = saved !== null && draft !== null && JSON.stringify(draft) !== JSON.stringify(toDraft(saved))
  const rootsValid = draft === null || draft.rootFolders.every((root) => root.path.trim().length > 0)

  function change(next: Draft) {
    setDraft(next)
    setError('')
    setNotice('')
    setTests(null)
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    event.stopPropagation()
    if (!draft || !canWrite) return
    setBusy('save')
    setError('')
    setNotice('')
    try {
      const payload: MovieConfig = {
        ...draft,
        metadataAPIKey: draft.metadataAPIKey.trim() || undefined,
        jellyfinAPIKey: draft.jellyfinAPIKey.trim() || undefined,
      }
      const config = await moviesApi.saveConfig(payload)
      setSaved(config)
      setDraft(toDraft(config))
      setTests(null)
      setNotice('Movie settings saved.')
      window.dispatchEvent(new Event('movies-changed'))
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function test() {
    setBusy('test')
    setTests(null)
    setError('')
    setNotice('')
    try {
      setTests(await moviesApi.testConfig())
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  if (!canRead) return null

  if (loading) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" />
        Loading movie settings…
      </p>
    )
  }

  if (!saved || !draft) {
    return (
      <div
        role="alert"
        className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
      >
        <span>{error || 'Could not load movie settings.'}</span>
        <Button variant="outline" size="sm" onClick={() => window.location.reload()}>
          Retry
        </Button>
      </div>
    )
  }

  const footer = canWrite ? (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4">
      <p className="text-xs text-muted-foreground" role="status">
        {dirty ? 'You have unsaved movie settings.' : 'Movie settings are saved.'}
      </p>
      <div className="flex gap-2">
        {section === 'connections' && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={Boolean(busy) || dirty}
            title={dirty ? 'Save or discard changes before testing.' : undefined}
            onClick={() => void test()}
          >
            {busy === 'test' ? (
              <LoaderCircle className="animate-spin motion-reduce:animate-none" />
            ) : (
              <RefreshCw />
            )}
            Test connections
          </Button>
        )}
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!dirty || Boolean(busy)}
          onClick={() => {
            setDraft(toDraft(saved))
            setError('')
            setNotice('')
            setTests(null)
          }}
        >
          Discard
        </Button>
        <Button type="submit" size="sm" disabled={!dirty || Boolean(busy) || !rootsValid}>
          {busy === 'save' ? (
            <LoaderCircle className="animate-spin motion-reduce:animate-none" />
          ) : (
            <Save />
          )}
          Save changes
        </Button>
      </div>
    </div>
  ) : (
    <p className="border-t border-border pt-4 text-xs text-muted-foreground">
      Your role can view movie settings but not change them.
    </p>
  )

  return (
    <form onSubmit={save} className="space-y-5">
      {error && (
        <div
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
        >
          {error}
        </div>
      )}
      {notice && (
        <p
          role="status"
          className="rounded-lg border border-emerald-500/20 bg-emerald-500/5 p-4 text-sm text-emerald-400"
        >
          <Check className="mr-2 inline size-4" />
          {notice}
        </p>
      )}

      {section === 'connections' && (
        <div className="grid items-start gap-5 xl:grid-cols-2">
          <Card className="shadow-none">
            <CardHeader className="border-b border-border">
              <CardTitle className="flex items-center gap-2">
                <KeyRound className="size-4 text-muted-foreground" />
                Metadata provider (OMDb)
              </CardTitle>
              <CardDescription>
                Search ratings, posters, and details for your movie and TV catalogs.
              </CardDescription>
            </CardHeader>
            <CardContent className="gap-5">
              <Field id="metadata-url" label="API URL" hint="OMDb-compatible endpoint used over HTTPS.">
                <Input
                  id="metadata-url"
                  type="url"
                  value={draft.metadataURL}
                  placeholder="https://www.omdbapi.com"
                  onChange={(event) => change({ ...draft, metadataURL: event.target.value })}
                />
              </Field>
              <SecretInput
                id="metadata-key"
                label="API key"
                configured={saved.metadataConfigured}
                value={draft.metadataAPIKey}
                hint="Stored on your server and never returned here. Leave blank to keep the saved key."
                onChange={(metadataAPIKey) => change({ ...draft, metadataAPIKey })}
              />
              <p className="flex items-center gap-2 text-xs text-muted-foreground">
                <ShieldCheck className="size-4 shrink-0" />
                Without a provider you can still add movies and series by entering details manually.
              </p>
            </CardContent>
          </Card>

          <Card className="shadow-none">
            <CardHeader className="border-b border-border">
              <CardTitle className="flex items-center gap-2">
                <RefreshCw className="size-4 text-muted-foreground" />
                Jellyfin & notifications
              </CardTitle>
              <CardDescription>
                Optional library refresh after imports and webhook delivery.
              </CardDescription>
            </CardHeader>
            <CardContent className="gap-5">
              <Field id="jellyfin-url" label="Jellyfin URL" hint="Constellarr asks Jellyfin to refresh the media library after an import.">
                <Input
                  id="jellyfin-url"
                  type="url"
                  value={draft.jellyfinURL}
                  placeholder="http://jellyfin:8096"
                  onChange={(event) => change({ ...draft, jellyfinURL: event.target.value })}
                />
              </Field>
              <SecretInput
                id="jellyfin-key"
                label="Jellyfin API key"
                configured={saved.jellyfinConfigured}
                value={draft.jellyfinAPIKey}
                hint="Leave blank to keep the saved key. Stored on your server only."
                onChange={(jellyfinAPIKey) => change({ ...draft, jellyfinAPIKey })}
              />
              <details className="border-t border-border pt-4">
                <summary className="cursor-pointer text-sm font-medium">Advanced: webhook notifications</summary>
                <div className="pt-4">
                  <Field
                    id="webhook-url"
                    label="Webhook URL"
                    hint="Optional. Constellarr posts a notification after imports and failures."
                  >
                    <Input
                      id="webhook-url"
                      type="url"
                      value={draft.webhookURL}
                      placeholder="https://example.com/hooks/constellarr"
                      onChange={(event) => change({ ...draft, webhookURL: event.target.value })}
                    />
                  </Field>
                </div>
              </details>
            </CardContent>
          </Card>

          {tests && (
            <ul className="space-y-2 xl:col-span-2">
              <TestResult label="Metadata provider" result={tests.metadata} />
              <TestResult label="Jellyfin" result={tests.jellyfin} />
            </ul>
          )}
        </div>
      )}

      {section === 'storage' && (
        <div className="space-y-5">
          <Card className="shadow-none">
            <CardHeader className="border-b border-border">
              <CardTitle className="flex items-center gap-2">
                <Folder className="size-4 text-muted-foreground" />
                Library folders
              </CardTitle>
              <CardDescription>Where imported movies are organized on your server. Add the folder that holds your movies; naming and automation live under advanced options.</CardDescription>
            </CardHeader>
            <CardContent className="gap-5">
              <div className="space-y-3">
                <span className="text-sm font-medium">Root folders</span>
                {draft.rootFolders.length === 0 && (
                  <p className="rounded-md border border-dashed border-border px-3 py-4 text-sm text-muted-foreground">
                    No root folders yet. Add the folder that holds your movies.
                  </p>
                )}
                <ul className="space-y-2">
                  {draft.rootFolders.map((root, index) => (
                    <li key={`${root.id || 'new'}-${index}`} className="flex items-center gap-2">
                      <Input
                        value={root.path}
                        aria-label={`Root folder ${index + 1} path`}
                        placeholder="/media/movies"
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
                  Add root folder
                </Button>
                <p className="text-xs text-muted-foreground">
                  New folders are registered when you save. Existing files are never moved by saving settings.
                </p>
              </div>
            </CardContent>
          </Card>

          <details className="rounded-xl border border-border">
            <summary className="cursor-pointer px-4 py-3 text-sm font-medium">Advanced: naming, imports, and automation</summary>
            <div className="grid items-start gap-5 border-t border-border p-4 xl:grid-cols-2">
              <Card className="shadow-none">
                <CardHeader className="border-b border-border">
                  <CardTitle className="flex items-center gap-2">
                    <Folder className="size-4 text-muted-foreground" />
                    Naming and imports
                  </CardTitle>
                  <CardDescription>How imported movies are named and placed. Changes apply to future imports.</CardDescription>
                </CardHeader>
                <CardContent className="gap-5">
                  <Field id="folder-template" label="Folder naming template" hint="Example: {title} ({year}).">
                    <Input
                      id="folder-template"
                      value={draft.folderTemplate}
                      onChange={(event) => change({ ...draft, folderTemplate: event.target.value })}
                    />
                  </Field>
                  <Field id="file-template" label="File naming template" hint="Example: {title} ({year}) {quality}.">
                    <Input
                      id="file-template"
                      value={draft.fileTemplate}
                      onChange={(event) => change({ ...draft, fileTemplate: event.target.value })}
                    />
                  </Field>
                  <Field id="import-mode" label="Import mode" hint="Hardlink keeps seeding data usable; move relocates the file.">
                    <Select
                      id="import-mode"
                      value={draft.importMode}
                      onChange={(event) => change({ ...draft, importMode: event.target.value })}
                    >
                      {optionsWith(draft.importMode, importModes).map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <Checkbox
                    id="write-nfo"
                    label="Write NFO sidecars"
                    description="Save movie details next to the file for other media tools."
                    checked={draft.writeNFO}
                    onChange={(writeNFO) => change({ ...draft, writeNFO })}
                  />
                </CardContent>
              </Card>

              <Card className="shadow-none">
                <CardHeader className="border-b border-border">
                  <CardTitle className="flex items-center gap-2">
                    <HardDrive className="size-4 text-muted-foreground" />
                    Automation
                  </CardTitle>
                  <CardDescription>Monitored movies without a file are searched automatically, and the best allowed release is downloaded. Turn monitoring off in a movie's details to stop this.</CardDescription>
                </CardHeader>
                <CardContent className="gap-5">
                  <Field id="poll-minutes" label="RSS check interval (minutes)" hint="How often Constellarr checks indexer feeds for monitored movies.">
                    <Input
                      id="poll-minutes"
                      type="number"
                      min="1"
                      className="max-w-32"
                      value={draft.pollMinutes}
                      onChange={(event) => change({ ...draft, pollMinutes: Number(event.target.value) || 0 })}
                    />
                  </Field>
                  <Field id="search-hours" label="Missing search interval (hours)" hint="How often monitored movies without a file are searched, and a release can be downloaded automatically.">
                    <Input
                      id="search-hours"
                      type="number"
                      min="1"
                      className="max-w-32"
                      value={draft.searchHours}
                      onChange={(event) => change({ ...draft, searchHours: Number(event.target.value) || 0 })}
                    />
                  </Field>
                  <Field id="minimum-availability" label="Minimum availability" hint="Do not search until the movie reaches this stage. Release dates come from the metadata provider.">
                    <Select
                      id="minimum-availability"
                      value={draft.minimumAvailability}
                      onChange={(event) => change({ ...draft, minimumAvailability: event.target.value })}
                    >
                      {optionsWith(draft.minimumAvailability, availabilityOptions).map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </Select>
                  </Field>
                  <Checkbox
                    id="retry-failed"
                    label="Retry failed downloads"
                    description="Failed releases are blocked, and alternate releases are tried on the next search."
                    checked={draft.retryFailed}
                    onChange={(retryFailed) => change({ ...draft, retryFailed })}
                  />
                </CardContent>
              </Card>
            </div>
          </details>
        </div>
      )}

      {footer}
    </form>
  )
}
