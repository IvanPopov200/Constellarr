import { useCallback, useEffect, useState } from 'react'
import { LoaderCircleIcon, PlusIcon, RefreshCwIcon, SaveIcon, ServerIcon, Trash2Icon, ZapIcon } from 'lucide-react'
import { useAuth } from '@/lib/auth-context'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { EmptyNote, ErrorNote, Field, Notice, Section, Select, Toggle } from '@/components/subtitles-ui'
import { errorMessage } from '@/lib/api'
import { discoveryApi, type AISettingsView } from '@/lib/discovery-api'
import {
  subtitlesApi,
  type SubtitleConfig,
  type SubtitleLanguagePreference,
  type SubtitleProfile,
  type SubtitleProvider,
  type SubtitleProviderStatus,
  type SubtitleProviderTest,
} from '@/lib/subtitles-api'

const languageSuggestion = 'en, de, fr, es, it, pt-BR, nl, pl, ru, uk, sv, no, da, fi, cs, tr, ar, he, ja, ko, zh, hi'

function emptyProvider(): SubtitleProvider {
  return {
    id: 'opensubtitles',
    name: 'OpenSubtitles',
    type: 'opensubtitles',
    endpoint: 'https://api.opensubtitles.com/api/v1',
    username: '',
    password: '',
    apiKey: '',
    enabled: true,
  }
}

function numberValue(value: string, fallback: number) {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

export function SubtitleSettings({ profiles, onChanged }: { profiles: SubtitleProfile[]; onChanged: () => void }) {
  const { can } = useAuth()
  const canRead = can('settings.read')
  const canWrite = can('settings.write')
  const [config, setConfig] = useState<SubtitleConfig | null>(null)
  const [statuses, setStatuses] = useState<SubtitleProviderStatus[]>([])
  const [tests, setTests] = useState<SubtitleProviderTest[]>([])
  const [sharedAI, setSharedAI] = useState<AISettingsView | null>(null)
  const [sharedAIError, setSharedAIError] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [profileDraft, setProfileDraft] = useState<SubtitleProfile>({
    id: '',
    name: '',
    languages: [{ code: 'en', forced: false, hi: false }],
    cutoff: 1,
  })

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!canRead) {
      setLoading(false)
      return
    }
    try {
      setLoading(true)
      const [loaded, providerStatuses] = await Promise.all([subtitlesApi.config(signal), subtitlesApi.providers(signal)])
      if (signal?.aborted) return
      setConfig(loaded)
      setStatuses(providerStatuses)
      setError('')
      // The translation provider is shared with recommendations; the subtitle UI only reads its status.
      discoveryApi
        .aiSettings(signal)
        .then((settings) => {
          if (!signal?.aborted) {
            setSharedAI(settings)
            setSharedAIError('')
          }
        })
        .catch((cause) => {
          if (!signal?.aborted) setSharedAIError(errorMessage(cause))
        })
    } catch (cause) {
      if (!signal?.aborted) setError(errorMessage(cause))
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [canRead])

  useEffect(() => {
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- state is set only after the fetch settles
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const patch = (update: Partial<SubtitleConfig>) => {
    setConfig((current) => (current ? { ...current, ...update } : current))
  }

  const save = async () => {
    if (!config) return
    setSaving(true)
    setError('')
    setNotice('')
    try {
      const saved = await subtitlesApi.saveConfig(config)
      setConfig(saved)
      setNotice('Subtitle settings saved.')
      await load()
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setSaving(false)
    }
  }

  const testProviders = async () => {
    setTesting(true)
    setError('')
    try {
      setTests(await subtitlesApi.testProviders())
      await load()
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setTesting(false)
    }
  }

  const updateProvider = (index: number, update: Partial<SubtitleProvider>) => {
    if (!config) return
    const providers = [...(config.providers ?? [])]
    providers[index] = { ...providers[index], ...update }
    patch({ providers })
  }

  const saveProfile = async () => {
    setError('')
    try {
      const languages = (profileDraft.languages ?? []).filter((item) => item.code.trim() !== '')
      const saved = await subtitlesApi.saveProfile({ ...profileDraft, languages })
      setNotice(`Saved profile ${saved.name}.`)
      setProfileDraft({ id: '', name: '', languages: [{ code: 'en', forced: false, hi: false }], cutoff: 1 })
      await load()
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const deleteProfile = async (id: string) => {
    setError('')
    try {
      await subtitlesApi.deleteProfile(id)
      await load()
      onChanged()
    } catch (cause) {
      setError(errorMessage(cause))
    }
  }

  const setLanguage = (index: number, update: Partial<SubtitleLanguagePreference>) => {
    const languages = [...(profileDraft.languages ?? [])]
    languages[index] = { ...languages[index], ...update }
    setProfileDraft({ ...profileDraft, languages })
  }

  if (!canRead) {
    return <EmptyNote>Subtitle settings require the settings read permission.</EmptyNote>
  }
  if (loading && !config) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircleIcon className="size-4 animate-spin motion-reduce:animate-none" /> Loading subtitle settings…
      </p>
    )
  }
  if (!config) {
    return <ErrorNote onRetry={() => load().catch((cause) => setError(errorMessage(cause)))}>{error || 'Subtitle settings are unavailable.'}</ErrorNote>
  }

  const providers = config.providers ?? []

  return (
    <div className="space-y-6">
      {error && <ErrorNote>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" onClick={save} disabled={saving || !canWrite}>
          {saving ? (
            <LoaderCircleIcon data-icon="inline-start" className="animate-spin motion-reduce:animate-none" />
          ) : (
            <SaveIcon data-icon="inline-start" />
          )}
          Save settings
        </Button>
        <Button size="sm" variant="outline" onClick={() => load().catch((cause) => setError(errorMessage(cause)))}>
          <RefreshCwIcon data-icon="inline-start" />
          Reload
        </Button>
        <Button size="sm" variant="outline" onClick={testProviders} disabled={testing || !canWrite}>
          <ZapIcon data-icon="inline-start" />
          Test providers
        </Button>
      </div>

      <fieldset disabled={!canWrite} className="min-w-0 space-y-6 border-0 p-0">
      <Section title="Automation">
        <div className="grid gap-3 sm:grid-cols-2">
          <Toggle
            label="Subtitles enabled"
            hint="The scheduler scans the library and searches for wanted languages."
            checked={config.enabled}
            onChange={(value) => patch({ enabled: value })}
          />
          <Toggle
            label="Search automatically"
            hint="Recurring search without an open browser."
            checked={config.autoSearch}
            onChange={(value) => patch({ autoSearch: value })}
          />
          <Toggle
            label="Download best result automatically"
            hint="Results at or above the score cutoff are queued for download."
            checked={config.autoDownload}
            onChange={(value) => patch({ autoDownload: value })}
          />
          <Field label="Default language profile">
            <Select value={config.defaultProfileId} onChange={(event) => patch({ defaultProfileId: event.target.value })}>
              <option value="">First profile</option>
              {profiles.map((profile) => (
                <option key={profile.id} value={profile.id}>
                  {profile.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Scan interval (minutes)">
            <Input
              value={String(config.scanMinutes)}
              inputMode="numeric"
              onChange={(event) => patch({ scanMinutes: numberValue(event.target.value, config.scanMinutes) })}
            />
          </Field>
          <Field label="Search interval (hours)">
            <Input
              value={String(config.searchIntervalHours)}
              inputMode="numeric"
              onChange={(event) => patch({ searchIntervalHours: numberValue(event.target.value, config.searchIntervalHours) })}
            />
          </Field>
          <Field label="Retry interval (minutes)">
            <Input
              value={String(config.retryMinutes)}
              inputMode="numeric"
              onChange={(event) => patch({ retryMinutes: numberValue(event.target.value, config.retryMinutes) })}
            />
          </Field>
          <Field label="Auto-download score cutoff" hint="Result scores combine language, variant, format, popularity, and rating.">
            <Input
              value={String(config.cutoffScore)}
              inputMode="numeric"
              onChange={(event) => patch({ cutoffScore: numberValue(event.target.value, config.cutoffScore) })}
            />
          </Field>
          <Field label="Provider timeout (seconds)">
            <Input
              value={String(config.providerTimeoutSeconds)}
              inputMode="numeric"
              onChange={(event) => patch({ providerTimeoutSeconds: numberValue(event.target.value, config.providerTimeoutSeconds) })}
            />
          </Field>
        </div>
      </Section>

      <Section
        title={`Providers (${providers.length})`}
        action={
          <Button
            size="xs"
            variant="outline"
            onClick={() => patch({ providers: [...providers, emptyProvider()] })}
            disabled={providers.length >= 8 || !canWrite}
          >
            <PlusIcon data-icon="inline-start" />
            Add provider
          </Button>
        }
      >
        {providers.length === 0 ? (
          <EmptyNote>Add an OpenSubtitles.com API key to search and download subtitles.</EmptyNote>
        ) : (
          <ul className="space-y-3">
            {providers.map((provider, index) => {
              const status = statuses.find((item) => item.providerId === provider.id)
              const test = tests.find((item) => item.providerId === provider.id)
              return (
                <li key={`${provider.id}-${index}`} className="space-y-3 rounded-lg border border-border p-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="flex items-center gap-2 text-sm font-medium">
                      <ServerIcon className="size-4 text-muted-foreground" aria-hidden="true" />
                      {provider.name || provider.id}
                      {status && (
                        <Badge variant={status.configured ? 'secondary' : 'outline'}>
                          {status.configured ? 'configured' : 'add API key'}
                        </Badge>
                      )}
                      {test && <Badge variant={test.ok ? 'secondary' : 'destructive'}>{test.ok ? 'reachable' : 'failed'}</Badge>}
                    </span>
                    <Button
                      size="xs"
                      variant="ghost"
                      onClick={() => patch({ providers: providers.filter((_, at) => at !== index) })}
                      disabled={!canWrite}
                      aria-label={`Remove ${provider.name}`}
                    >
                      <Trash2Icon data-icon="inline-start" />
                      Remove
                    </Button>
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label="Provider id">
                      <Input value={provider.id} onChange={(event) => updateProvider(index, { id: event.target.value })} />
                    </Field>
                    <Field label="Display name">
                      <Input value={provider.name} onChange={(event) => updateProvider(index, { name: event.target.value })} />
                    </Field>
                    <Field label="API endpoint">
                      <Input value={provider.endpoint} onChange={(event) => updateProvider(index, { endpoint: event.target.value })} />
                    </Field>
                    <Field label="API key" hint={provider.apiKeySet ? 'A key is stored; leave blank to keep it.' : 'Required for OpenSubtitles.com.'}>
                      <Input
                        type="password"
                        value={provider.apiKey ?? ''}
                        onChange={(event) => updateProvider(index, { apiKey: event.target.value })}
                        autoComplete="off"
                      />
                    </Field>
                    <Field label="Username" hint="Optional; sign-in raises the daily download quota.">
                      <Input value={provider.username} onChange={(event) => updateProvider(index, { username: event.target.value })} />
                    </Field>
                    <Field label="Password" hint={provider.passwordSet ? 'A password is stored; leave blank to keep it.' : ''}>
                      <Input
                        type="password"
                        value={provider.password ?? ''}
                        onChange={(event) => updateProvider(index, { password: event.target.value })}
                        autoComplete="off"
                      />
                    </Field>
                  </div>
                  <Toggle
                    label="Enabled"
                    checked={provider.enabled}
                    onChange={(value) => updateProvider(index, { enabled: value })}
                  />
                  {status?.lastError && <p className="break-words text-xs text-destructive">{status.lastError}</p>}
                  {status?.quotaRemaining !== undefined && (
                    <p className="text-xs text-muted-foreground">
                      {status.quotaRemaining} downloads remain{status.quotaReset ? ` until ${status.quotaReset}` : ''}
                    </p>
                  )}
                  {test && !test.ok && <p className="break-words text-xs text-destructive">{test.error}</p>}
                  {test?.ok && test.message && <p className="break-words text-xs text-muted-foreground">{test.message}</p>}
                </li>
              )
            })}
          </ul>
        )}
      </Section>

      <Section title={`Language profiles (${profiles.length})`}>
        <ul className="divide-y divide-border rounded-lg border border-border">
          {profiles.map((profile) => (
            <li key={profile.id} className="flex flex-wrap items-center justify-between gap-2 p-3">
              <div className="min-w-0">
                <p className="text-sm font-medium">{profile.name}</p>
                <p className="text-xs text-muted-foreground">
                  {(profile.languages ?? [])
                    .map((item) => item.code + (item.forced ? ' forced' : '') + (item.hi ? ' HI' : ''))
                    .join(', ')}{' '}
                  · cutoff {profile.cutoff}
                </p>
              </div>
              <div className="flex gap-1">
                <Button
                  size="xs"
                  variant="ghost"
                  onClick={() => setProfileDraft({ ...profile, languages: profile.languages ?? [] })}
                >
                  Edit
                </Button>
                <Button size="xs" variant="ghost" onClick={() => deleteProfile(profile.id)} disabled={!canWrite} aria-label={`Delete ${profile.name}`}>
                  <Trash2Icon data-icon="inline-start" />
                </Button>
              </div>
            </li>
          ))}
        </ul>
        <div className="space-y-3 rounded-lg border border-border p-3">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Profile name">
              <Input value={profileDraft.name} onChange={(event) => setProfileDraft({ ...profileDraft, name: event.target.value })} />
            </Field>
            <Field label="Profile id" hint="Leave blank to generate one.">
              <Input value={profileDraft.id} onChange={(event) => setProfileDraft({ ...profileDraft, id: event.target.value })} />
            </Field>
            <Field label="Cutoff" hint="Stop searching once this many languages are satisfied.">
              <Input
                value={String(profileDraft.cutoff)}
                inputMode="numeric"
                onChange={(event) => setProfileDraft({ ...profileDraft, cutoff: numberValue(event.target.value, 1) })}
              />
            </Field>
          </div>
          <div className="space-y-2">
            {(profileDraft.languages ?? []).map((language, index) => (
              <div key={index} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_auto_auto]">
                <Input
                  value={language.code}
                  placeholder={languageSuggestion.split(', ')[index % 4]}
                  onChange={(event) => setLanguage(index, { code: event.target.value })}
                  aria-label={`Language ${index + 1}`}
                />
                <Toggle label="Forced" checked={language.forced} onChange={(value) => setLanguage(index, { forced: value })} />
                <Toggle label="HI" checked={language.hi} onChange={(value) => setLanguage(index, { hi: value })} />
                <Button
                  size="xs"
                  variant="ghost"
                  onClick={() =>
                    setProfileDraft({
                      ...profileDraft,
                      languages: (profileDraft.languages ?? []).filter((_, at) => at !== index),
                    })
                  }
                >
                  <Trash2Icon data-icon="inline-start" />
                  Remove
                </Button>
              </div>
            ))}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              size="xs"
              variant="outline"
              onClick={() =>
                setProfileDraft({
                  ...profileDraft,
                  languages: [...(profileDraft.languages ?? []), { code: '', forced: false, hi: false }],
                })
              }
            >
              <PlusIcon data-icon="inline-start" />
              Add language
            </Button>
            <Button size="xs" onClick={saveProfile} disabled={!canWrite}>
              <SaveIcon data-icon="inline-start" />
              Save profile
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">Suggestions: {languageSuggestion}</p>
        </div>
      </Section>

      <Section title="Synchronization helper" action={<span className="text-xs text-muted-foreground">Bundled ffmpeg + ffsubsync</span>}>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="ffsubsync path" hint="Leave blank to use ffsubsync from PATH.">
            <Input value={config.sync.helperPath} onChange={(event) => patch({ sync: { ...config.sync, helperPath: event.target.value } })} />
          </Field>
          <Field label="ffmpeg directory" hint="Optional; used for embedded track extraction and audio references.">
            <Input value={config.sync.ffmpegPath} onChange={(event) => patch({ sync: { ...config.sync, ffmpegPath: event.target.value } })} />
          </Field>
          <Field label="Helper timeout (seconds)">
            <Input
              value={String(config.sync.timeoutSeconds)}
              inputMode="numeric"
              onChange={(event) => patch({ sync: { ...config.sync, timeoutSeconds: numberValue(event.target.value, config.sync.timeoutSeconds) } })}
            />
          </Field>
          <Field label="Max offset (seconds)">
            <Input
              value={String(config.sync.maxOffsetSeconds)}
              inputMode="decimal"
              onChange={(event) => patch({ sync: { ...config.sync, maxOffsetSeconds: numberValue(event.target.value, config.sync.maxOffsetSeconds) } })}
            />
          </Field>
          <Field label="Minimum alignment score" hint="Passed with --skip-sync-on-low-quality.">
            <Input
              value={String(config.sync.minScore)}
              inputMode="decimal"
              onChange={(event) => patch({ sync: { ...config.sync, minScore: numberValue(event.target.value, config.sync.minScore) } })}
            />
          </Field>
          <Field label="Quality max offset (seconds)">
            <Input
              value={String(config.sync.qualityMaxOffsetSeconds)}
              inputMode="decimal"
              onChange={(event) =>
                patch({ sync: { ...config.sync, qualityMaxOffsetSeconds: numberValue(event.target.value, config.sync.qualityMaxOffsetSeconds) } })
              }
            />
          </Field>
          <Field label="Max frame rate deviation">
            <Input
              value={String(config.sync.maxFramerateDeviation)}
              inputMode="decimal"
              onChange={(event) =>
                patch({ sync: { ...config.sync, maxFramerateDeviation: numberValue(event.target.value, config.sync.maxFramerateDeviation) } })
              }
            />
          </Field>
          <Field label="Voice activity detector">
            <Select value={config.sync.vad} onChange={(event) => patch({ sync: { ...config.sync, vad: event.target.value } })}>
              <option value="">Default (webrtc)</option>
              <option value="webrtc">webrtc</option>
              <option value="auditok">auditok</option>
              <option value="silero">silero</option>
              <option value="fused">fused</option>
            </Select>
          </Field>
          <Field label="Audio reference limit (seconds)" hint="Bounds ffmpeg extraction when a specific audio track is chosen.">
            <Input
              value={String(config.sync.audioReferenceSeconds)}
              inputMode="numeric"
              onChange={(event) => patch({ sync: { ...config.sync, audioReferenceSeconds: numberValue(event.target.value, config.sync.audioReferenceSeconds) } })}
            />
          </Field>
          <Field label="Max embedded stream index">
            <Input
              value={String(config.sync.maxEmbeddedStreamIndex)}
              inputMode="numeric"
              onChange={(event) =>
                patch({ sync: { ...config.sync, maxEmbeddedStreamIndex: numberValue(event.target.value, config.sync.maxEmbeddedStreamIndex) } })
              }
            />
          </Field>
        </div>
      </Section>

      <Section
        title="AI translation"
        action={<span className="text-xs text-muted-foreground">Shared provider from Connections</span>}
      >
        <div className="space-y-2 rounded-lg border border-border p-3">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="font-medium">Translation provider</span>
            {sharedAI?.model ? (
              <Badge variant="secondary">{sharedAI.model}</Badge>
            ) : (
              <Badge variant="outline">not configured</Badge>
            )}
            {sharedAI?.apiKeyConfigured && <Badge variant="outline">API key stored</Badge>}
          </div>
          <p className="text-xs text-muted-foreground">
            Subtitle translation uses the same OpenAI-compatible service as recommendations. Configure the base URL, key,
            and model once in <a className="underline" href="#connections">Connections</a>.
          </p>
          {sharedAI?.baseURL && <p className="break-all text-xs text-muted-foreground">{sharedAI.baseURL}</p>}
          {sharedAIError && <p className="break-words text-xs text-amber-500">{sharedAIError}</p>}
        </div>
        <Toggle
          label="Allow subtitle translation"
          hint="Cue integrity, language, and formatting are validated before anything is saved."
          checked={config.ai.enabled}
          onChange={(value) => patch({ ai: { ...config.ai, enabled: value } })}
          disabled={!canWrite}
        />
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Request timeout (seconds)">
            <Input
              value={String(config.ai.timeoutSeconds)}
              inputMode="numeric"
              disabled={!canWrite}
              onChange={(event) => patch({ ai: { ...config.ai, timeoutSeconds: numberValue(event.target.value, config.ai.timeoutSeconds) } })}
            />
          </Field>
          <Field label="Max tokens per request">
            <Input
              value={String(config.ai.maxTokens)}
              inputMode="numeric"
              disabled={!canWrite}
              onChange={(event) => patch({ ai: { ...config.ai, maxTokens: numberValue(event.target.value, config.ai.maxTokens) } })}
            />
          </Field>
          <Field label="Max requests per translation">
            <Input
              value={String(config.ai.maxRequests)}
              inputMode="numeric"
              disabled={!canWrite}
              onChange={(event) => patch({ ai: { ...config.ai, maxRequests: numberValue(event.target.value, config.ai.maxRequests) } })}
            />
          </Field>
          <Field label="Max total tokens" hint="A translation fails closed when the budget is exhausted.">
            <Input
              value={String(config.ai.maxTotalTokens)}
              inputMode="numeric"
              disabled={!canWrite}
              onChange={(event) => patch({ ai: { ...config.ai, maxTotalTokens: numberValue(event.target.value, config.ai.maxTotalTokens) } })}
            />
          </Field>
          <Field label="Characters per chunk">
            <Input
              value={String(config.ai.maxCharacters)}
              inputMode="numeric"
              disabled={!canWrite}
              onChange={(event) => patch({ ai: { ...config.ai, maxCharacters: numberValue(event.target.value, config.ai.maxCharacters) } })}
            />
          </Field>
          <Field label="Temperature">
            <Input
              value={String(config.ai.temperature)}
              inputMode="decimal"
              disabled={!canWrite}
              onChange={(event) => patch({ ai: { ...config.ai, temperature: numberValue(event.target.value, config.ai.temperature) } })}
            />
          </Field>
        </div>
        {!canWrite && (
          <p className="text-xs text-muted-foreground">Your role can view subtitle settings but not change them.</p>
        )}
      </Section>
      </fieldset>
    </div>
  )
}
