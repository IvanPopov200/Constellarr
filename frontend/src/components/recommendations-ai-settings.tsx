import { useEffect, useState, type FormEvent } from 'react'
import { Check, Eye, EyeOff, KeyRound, LoaderCircle, RefreshCw, Save, Sparkles } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { ErrorNote, Field, LoadingNote, Notice, Choice } from '@/components/requests-shared'
import { errorMessage } from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import { discoveryApi, type AISettingsView, type AITestResult } from '@/lib/discovery-api'

type Draft = AISettingsView & { apiKey: string }

function toDraft(settings: AISettingsView): Draft {
  return { ...settings, apiKey: '' }
}

export function AISettings() {
  const { can } = useAuth()
  const canRead = can(accessPermissions.settingsRead)
  const canWrite = can(accessPermissions.settingsWrite)
  const [saved, setSaved] = useState<AISettingsView | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [models, setModels] = useState<string[]>([])
  const [test, setTest] = useState<AITestResult | null>(null)
  const [busy, setBusy] = useState<'save' | 'test' | 'models' | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [loading, setLoading] = useState(true)
  const [showKey, setShowKey] = useState(false)
  const dirty = saved !== null && draft !== null && JSON.stringify(draft) !== JSON.stringify(toDraft(saved))

  useEffect(() => {
    if (!canRead) return undefined
    const controller = new AbortController()
    discoveryApi
      .aiSettings(controller.signal)
      .then((settings) => {
        setSaved(settings)
        setDraft(toDraft(settings))
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [canRead])

  function change(next: Draft) {
    if (!canWrite) return
    setDraft(next)
    setTest(null)
    setNotice('')
    setError('')
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (!draft || !canWrite) return
    setBusy('save')
    setError('')
    setNotice('')
    try {
      const settings = await discoveryApi.saveAISettings(draft)
      setSaved(settings)
      setDraft(toDraft(settings))
      setNotice('AI settings saved. Recommendations and subtitle translation now use this provider.')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function runTest() {
    if (!canWrite) return
    setBusy('test')
    setTest(null)
    setError('')
    setNotice('')
    try {
      setTest(await discoveryApi.testAI())
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function fetchModels() {
    if (!canWrite) return
    setBusy('models')
    setError('')
    setNotice('')
    try {
      const list = await discoveryApi.aiModels()
      setModels(list)
      if (list.length === 0) setError('The provider exposed no models. Many compatible servers do not implement /models.')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  if (!canRead) return null

  const ready = Boolean(saved?.baseURL && saved.model)
  return (
    <Card className="shadow-none">
      <CardHeader className="border-b border-border">
        <CardTitle className="flex flex-wrap items-center justify-between gap-2">
          <span className="flex items-center gap-2">
            <Sparkles className="size-4 text-muted-foreground" aria-hidden="true" />
            AI provider
          </span>
          <span className="flex items-center gap-2">
            {saved && <Badge variant="ghost" className="text-muted-foreground">{saved.apiKeyConfigured ? 'API key saved' : 'No API key (local endpoint)'}</Badge>}
            <Badge variant="outline">{ready ? 'Configured' : 'Setup needed'}</Badge>
          </span>
        </CardTitle>
        <CardDescription>
          One OpenAI-compatible endpoint for recommendations and subtitle translation. Credentials stay on the server
          and are never returned to the browser.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {loading && <LoadingNote>Loading AI settings…</LoadingNote>}
        {error && <ErrorNote onRetry={() => window.location.reload()}>{error}</ErrorNote>}
        {notice && <Notice>{notice}</Notice>}
        {saved && draft && (
          <form className="space-y-4" onSubmit={save}>
            <div className="grid gap-4 lg:grid-cols-2">
              <Field label="Base URL" hint="For example https://api.openai.com/v1 or a local compatible server.">
                <Input
                  type="url"
                  required
                  value={draft.baseURL}
                  placeholder="https://api.openai.com/v1"
                  disabled={!canWrite}
                  onChange={(event) => change({ ...draft, baseURL: event.target.value })}
                />
              </Field>
              <Field label="Model" hint="The model name the endpoint expects.">
                <Input required value={draft.model} placeholder="gpt-4o-mini" disabled={!canWrite} onChange={(event) => change({ ...draft, model: event.target.value })} />
              </Field>
              <div className="space-y-2">
                <label htmlFor="ai-key" className="text-sm font-medium">
                  API key
                </label>
                <div className="relative">
                  <Input
                    id="ai-key"
                    type={showKey ? 'text' : 'password'}
                    autoComplete="new-password"
                    value={draft.apiKey}
                    placeholder={saved.apiKeyConfigured ? 'Saved key (leave blank to keep)' : 'Optional for a local endpoint'}
                    className="pr-11"
                    disabled={!canWrite}
                    onChange={(event) => change({ ...draft, apiKey: event.target.value })}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    className="absolute top-0.5 right-1"
                    aria-label={showKey ? 'Hide API key' : 'Show API key'}
                    aria-pressed={showKey}
                    disabled={!canWrite}
                    onClick={() => setShowKey(!showKey)}
                  >
                    {showKey ? <EyeOff /> : <Eye />}
                  </Button>
                </div>
                <p className="flex items-center gap-2 text-xs text-muted-foreground">
                  <KeyRound className="size-3.5" aria-hidden="true" />
                  {saved.apiKeyConfigured
                    ? 'A key is saved. Enter a new value only to replace it.'
                    : 'Leave blank for a local endpoint that accepts unauthenticated requests; the key is never returned to the browser.'}
                </p>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <Field label="Max tokens" hint="Response budget per request.">
                  <Input
                    type="number"
                    min={1}
                    max={4096}
                    value={draft.maxTokens}
                    disabled={!canWrite}
                    onChange={(event) => change({ ...draft, maxTokens: Number(event.target.value) || 1 })}
                  />
                </Field>
                <Field label="Temperature" hint="0 is deterministic, 2 is adventurous.">
                  <Input
                    type="number"
                    min={0}
                    max={2}
                    step={0.1}
                    value={draft.temperature}
                    disabled={!canWrite}
                    onChange={(event) => change({ ...draft, temperature: Number(event.target.value) })}
                  />
                </Field>
              </div>
            </div>

            {models.length > 0 && (
              <Field label="Available models" hint="Fetched from the provider.">
                <Choice value={draft.model} disabled={!canWrite} onChange={(event) => change({ ...draft, model: event.target.value })}>
                  {models.map((model) => (
                    <option key={model} value={model}>
                      {model}
                    </option>
                  ))}
                </Choice>
              </Field>
            )}

            {test && (
              <p role={test.ok ? 'status' : 'alert'} className={`rounded-lg border p-3 text-sm ${test.ok ? 'border-emerald-500/20 bg-emerald-500/5 text-emerald-400' : 'border-destructive/30 bg-destructive/10 text-destructive'}`}>
                {test.ok ? (
                  <>
                    <Check className="mr-2 inline size-4" aria-hidden="true" />
                    Connection verified{test.models?.length ? ` · ${test.models.length} models` : ''}
                  </>
                ) : (
                  test.error || 'Connection failed. Check the endpoint, model, and credentials.'
                )}
              </p>
            )}

            {canWrite ? (
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-xs text-muted-foreground" role="status">
                  {dirty ? 'You have unsaved changes.' : 'All changes saved.'}
                </p>
                <div className="flex flex-wrap gap-2">
                  <Button type="button" variant="outline" size="sm" disabled={busy !== null || dirty} title={dirty ? 'Save changes before testing.' : undefined} onClick={() => void runTest()}>
                    {busy === 'test' ? <LoaderCircle className="animate-spin" /> : <RefreshCw />}
                    Test connection
                  </Button>
                  <Button type="button" variant="outline" size="sm" disabled={busy !== null || dirty} title={dirty ? 'Save changes before fetching models.' : undefined} onClick={() => void fetchModels()}>
                    {busy === 'models' ? <LoaderCircle className="animate-spin" /> : <Sparkles />}
                    Fetch models
                  </Button>
                  <Button type="submit" size="sm" disabled={!dirty || busy !== null}>
                    {busy === 'save' ? <LoaderCircle className="animate-spin" /> : <Save />}
                    Save
                  </Button>
                </div>
              </div>
            ) : (
              <p className="border-t border-border pt-4 text-xs text-muted-foreground">
                Your role can view the AI provider settings but not change them.
              </p>
            )}
          </form>
        )}
      </CardContent>
    </Card>
  )
}
