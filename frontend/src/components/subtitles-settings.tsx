import { useCallback, useEffect, useState } from 'react'
import { LoaderCircleIcon, PlusIcon, RefreshCwIcon, SaveIcon, ServerIcon, Trash2Icon, ZapIcon } from 'lucide-react'
import { useAuth } from '@/lib/auth-context'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  ActionNote,
  Disclosure,
  EmptyNote,
  ErrorNote,
  Field,
  Section,
  Select,
  Toggle,
  languageOptions,
  variantText,
} from '@/components/subtitles-ui'
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

const languageListId = 'subtitle-language-codes'

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

function languageVariant(item: SubtitleLanguagePreference) {
  return variantText({ language: item.code, forced: item.forced, hi: item.hi })
}

type Feedback = { scope: 'config' | 'test' | 'profile'; tone: 'success' | 'error'; message: string }

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
  const [feedback, setFeedback] = useState<Feedback | null>(null)
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
    setFeedback(null)
    try {
      const saved = await subtitlesApi.saveConfig(config)
      setConfig(saved)
      setFeedback({ scope: 'config', tone: 'success', message: 'Subtitle settings saved.' })
      await load()
      onChanged()
    } catch (cause) {
      setFeedback({ scope: 'config', tone: 'error', message: errorMessage(cause) })
    } finally {
      setSaving(false)
    }
  }

  const testProviders = async () => {
    setTesting(true)
    setError('')
    setFeedback(null)
    try {
      const results = await subtitlesApi.testProviders()
      setTests(results)
      const ready = results.filter((item) => item.ok).length
      setFeedback({
        scope: 'test',
        tone: ready > 0 ? 'success' : 'error',
        message:
          results.length === 0
            ? 'No enabled provider was available to test.'
            : `${ready} of ${results.length} enabled ${results.length === 1 ? 'provider is' : 'providers are'} reachable.`,
      })
      await load()
    } catch (cause) {
      setFeedback({ scope: 'test', tone: 'error', message: errorMessage(cause) })
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
    setFeedback(null)
    try {
      const languages = (profileDraft.languages ?? []).filter((item) => item.code.trim() !== '')
      const saved = await subtitlesApi.saveProfile({ ...profileDraft, languages })
      setFeedback({ scope: 'profile', tone: 'success', message: `Saved profile ${saved.name}.` })
      setProfileDraft({ id: '', name: '', languages: [{ code: 'en', forced: false, hi: false }], cutoff: 1 })
      await load()
      onChanged()
    } catch (cause) {
      setFeedback({ scope: 'profile', tone: 'error', message: errorMessage(cause) })
    }
  }

  const deleteProfile = async (id: string) => {
    setError('')
    setFeedback(null)
    try {
      await subtitlesApi.deleteProfile(id)
      setFeedback({ scope: 'profile', tone: 'success', message: 'Profile deleted.' })
      await load()
      onChanged()
    } catch (cause) {
      setFeedback({ scope: 'profile', tone: 'error', message: errorMessage(cause) })
    }
  }

  const setLanguage = (index: number, update: Partial<SubtitleLanguagePreference>) => {
    const languages = [...(profileDraft.languages ?? [])]
    languages[index] = { ...languages[index], ...update }
    setProfileDraft({ ...profileDraft, languages })
  }

  const noteFor = (scope: Feedback['scope']) =>
    feedback?.scope === scope ? <ActionNote tone={feedback.tone}>{feedback.message}</ActionNote> : null

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
  const draftLanguages = profileDraft.languages ?? []

  return (
    <div className="space-y-6">
      {error && <ErrorNote>{error}</ErrorNote>}
      <div className="space-y-2">
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
        {noteFor('config')}
        {noteFor('test')}
        {!canWrite && <ActionNote>Your role can view subtitle settings but not change them.</ActionNote>}
      </div>

      <fieldset disabled={!canWrite} className="min-w-0 space-y-6 border-0 p-0">
      <Section title="Automatic subtitles">
        <div className="grid gap-3 sm:grid-cols-2">
          <Toggle
            label="Find subtitles automatically"
            hint="Constellarr scans the library and searches for the languages your profiles ask for."
            checked={config.enabled}
            onChange={(value) => patch({ enabled: value })}
          />
          <Toggle
            label="Keep searching in the background"
            hint="Recurring search without an open browser."
            checked={config.autoSearch}
            onChange={(value) => patch({ autoSearch: value })}
          />
          <Toggle
            label="Download the best result automatically"
            hint="Results at or above the score cutoff are downloaded without asking."
            checked={config.autoDownload}
            onChange={(value) => patch({ autoDownload: value })}
          />
          <Field label="Default language profile" hint="Used for videos without their own profile.">
            <Select value={config.defaultProfileId} onChange={(event) => patch({ defaultProfileId: event.target.value })}>
              <option value="">First profile</option>
              {profiles.map((profile) => (
                <option key={profile.id} value={profile.id}>
                  {profile.name}
                </option>
              ))}
            </Select>
          </Field>
        </div>
      </Section>

      <Section title={`Languages (${profiles.length} ${profiles.length === 1 ? 'profile' : 'profiles'})`}>
        {profiles.length === 0 ? (
          <EmptyNote>
            No language profile yet. A profile lists the subtitle languages you want, for example English first and German
            as a second choice.
          </EmptyNote>
        ) : (
          <ul className="divide-y divide-border rounded-lg border border-border">
            {profiles.map((profile) => (
              <li key={profile.id} className="flex flex-wrap items-center justify-between gap-2 p-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium">{profile.name}</p>
                  <p className="text-xs text-muted-foreground">
                    {(profile.languages ?? []).map(languageVariant).join(', ') || 'no languages yet'} · stops at {profile.cutoff}
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
                  <Button
                    size="xs"
                    variant="ghost"
                    onClick={() => deleteProfile(profile.id)}
                    disabled={!canWrite}
                    aria-label={`Delete ${profile.name}`}
                  >
                    <Trash2Icon data-icon="inline-start" />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <div className="space-y-3 rounded-lg border border-border p-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Profile name">
              <Input value={profileDraft.name} onChange={(event) => setProfileDraft({ ...profileDraft, name: event.target.value })} />
            </Field>
            <Field label="Stop after this many languages" hint="Constellarr keeps looking until this many are found.">
              <Input
                value={String(profileDraft.cutoff)}
                inputMode="numeric"
                onChange={(event) => setProfileDraft({ ...profileDraft, cutoff: numberValue(event.target.value, 1) })}
              />
            </Field>
          </div>
          <div className="space-y-2">
            {draftLanguages.map((language, index) => (
              <div key={index} className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_auto_auto]">
                <Input
                  value={language.code}
                  list={languageListId}
                  placeholder="Choose or type a language"
                  onChange={(event) => setLanguage(index, { code: event.target.value })}
                  aria-label={`Language ${index + 1}`}
                />
                <Toggle label="Forced" checked={language.forced} onChange={(value) => setLanguage(index, { forced: value })} />
                <Toggle label="Hearing impaired" checked={language.hi} onChange={(value) => setLanguage(index, { hi: value })} />
                <Button
                  size="xs"
                  variant="ghost"
                  onClick={() =>
                    setProfileDraft({
                      ...profileDraft,
                      languages: draftLanguages.filter((_, at) => at !== index),
                    })
                  }
                >
                  <Trash2Icon data-icon="inline-start" />
                  Remove
                </Button>
              </div>
            ))}
          </div>
          <datalist id={languageListId}>
            {languageOptions(draftLanguages.map((language) => language.code)).map((option) => (
              <option key={option.code} value={option.code}>
                {option.name}
              </option>
            ))}
          </datalist>
          <div className="flex flex-wrap gap-2">
            <Button
              size="xs"
              variant="outline"
              onClick={() =>
                setProfileDraft({
                  ...profileDraft,
                  languages: [...draftLanguages, { code: '', forced: false, hi: false }],
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
          {noteFor('profile')}
          <Disclosure label="Advanced options" variant="inline">
            <Field label="Profile id" hint="Leave blank to generate one. Only needed when another tool refers to this profile by id.">
              <Input value={profileDraft.id} onChange={(event) => setProfileDraft({ ...profileDraft, id: event.target.value })} />
            </Field>
          </Disclosure>
        </div>
      </Section>

      <Section
        title={`Subtitle provider (${providers.length})`}
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
          <EmptyNote>Add an OpenSubtitles.com API key so Constellarr can search for and download subtitles.</EmptyNote>
        ) : (
          <ul className="space-y-3">
            {providers.map((provider, index) => {
              const status = statuses.find((item) => item.providerId === provider.id)
              const test = tests.find((item) => item.providerId === provider.id)
              return (
                <li key={`${provider.id}-${index}`} className="space-y-3 rounded-lg border border-border p-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                      <ServerIcon className="size-4 text-muted-foreground" aria-hidden="true" />
                      {provider.name || provider.id}
                      {status && (
                        <Badge variant={status.configured ? 'secondary' : 'outline'}>
                          {status.configured ? 'ready' : 'needs an API key'}
                        </Badge>
                      )}
                      {test && (
                        <Badge variant={test.ok ? 'secondary' : 'destructive'}>
                          {test.ok ? 'reachable' : 'not reachable'}
                        </Badge>
                      )}
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
                    <Field
                      label="API key"
                      hint={provider.apiKeySet ? 'A key is stored; leave blank to keep it.' : 'Required for OpenSubtitles.com.'}
                    >
                      <Input
                        type="password"
                        value={provider.apiKey ?? ''}
                        onChange={(event) => updateProvider(index, { apiKey: event.target.value })}
                        autoComplete="off"
                      />
                    </Field>
                    <Field label="Username" hint="Optional; signing in raises the daily download limit.">
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
                    <div className="flex items-end pb-2">
                      <Toggle
                        label="Use this provider"
                        checked={provider.enabled}
                        onChange={(value) => updateProvider(index, { enabled: value })}
                      />
                    </div>
                  </div>
                  {status?.lastError && <ActionNote tone="error">{status.lastError}</ActionNote>}
                  {status?.quotaRemaining !== undefined && (
                    <ActionNote>
                      {status.quotaRemaining} downloads remain{status.quotaReset ? ` until ${status.quotaReset}` : ''}
                    </ActionNote>
                  )}
                  {test && !test.ok && <ActionNote tone="error">{test.error}</ActionNote>}
                  {test?.ok && test.message && <ActionNote>{test.message}</ActionNote>}
                  <Disclosure label="Advanced options" variant="inline" detail={provider.endpoint}>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <Field label="Provider id" hint="Identifies this provider in jobs and history.">
                        <Input value={provider.id} onChange={(event) => updateProvider(index, { id: event.target.value })} />
                      </Field>
                      <Field label="API endpoint">
                        <Input value={provider.endpoint} onChange={(event) => updateProvider(index, { endpoint: event.target.value })} />
                      </Field>
                    </div>
                  </Disclosure>
                </li>
              )
            })}
          </ul>
        )}
      </Section>

      <Section title="Advanced helpers">
        <p className="text-sm text-muted-foreground">
          Scheduling, the bundled synchronization tools, and AI translation limits.
        </p>

        <Disclosure label="Search schedule and scoring" detail={`scan every ${config.scanMinutes} min`}>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Scan the library every (minutes)">
              <Input
                value={String(config.scanMinutes)}
                inputMode="numeric"
                onChange={(event) => patch({ scanMinutes: numberValue(event.target.value, config.scanMinutes) })}
              />
            </Field>
            <Field label="Search again every (hours)">
              <Input
                value={String(config.searchIntervalHours)}
                inputMode="numeric"
                onChange={(event) => patch({ searchIntervalHours: numberValue(event.target.value, config.searchIntervalHours) })}
              />
            </Field>
            <Field label="Wait before retrying (minutes)">
              <Input
                value={String(config.retryMinutes)}
                inputMode="numeric"
                onChange={(event) => patch({ retryMinutes: numberValue(event.target.value, config.retryMinutes) })}
              />
            </Field>
            <Field label="Auto-download score cutoff" hint="Scores combine language, variant, format, popularity, and rating.">
              <Input
                value={String(config.cutoffScore)}
                inputMode="numeric"
                onChange={(event) => patch({ cutoffScore: numberValue(event.target.value, config.cutoffScore) })}
              />
            </Field>
            <Field label="Give up on a provider after (seconds)">
              <Input
                value={String(config.providerTimeoutSeconds)}
                inputMode="numeric"
                onChange={(event) => patch({ providerTimeoutSeconds: numberValue(event.target.value, config.providerTimeoutSeconds) })}
              />
            </Field>
          </div>
        </Disclosure>

        <Disclosure label="Synchronization helper" detail="Bundled ffmpeg + ffsubsync">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="ffsubsync path" hint="Leave blank to use ffsubsync from PATH.">
              <Input value={config.sync.helperPath} onChange={(event) => patch({ sync: { ...config.sync, helperPath: event.target.value } })} />
            </Field>
            <Field label="ffmpeg directory" hint="Optional; used for embedded track extraction and audio references.">
              <Input value={config.sync.ffmpegPath} onChange={(event) => patch({ sync: { ...config.sync, ffmpegPath: event.target.value } })} />
            </Field>
            <Field label="Stop the helper after (seconds)">
              <Input
                value={String(config.sync.timeoutSeconds)}
                inputMode="numeric"
                onChange={(event) => patch({ sync: { ...config.sync, timeoutSeconds: numberValue(event.target.value, config.sync.timeoutSeconds) } })}
              />
            </Field>
            <Field label="Largest offset to try (seconds)">
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
            <Field label="Quality check offset (seconds)">
              <Input
                value={String(config.sync.qualityMaxOffsetSeconds)}
                inputMode="decimal"
                onChange={(event) =>
                  patch({ sync: { ...config.sync, qualityMaxOffsetSeconds: numberValue(event.target.value, config.sync.qualityMaxOffsetSeconds) } })
                }
              />
            </Field>
            <Field label="Allowed frame rate difference">
              <Input
                value={String(config.sync.maxFramerateDeviation)}
                inputMode="decimal"
                onChange={(event) =>
                  patch({ sync: { ...config.sync, maxFramerateDeviation: numberValue(event.target.value, config.sync.maxFramerateDeviation) } })
                }
              />
            </Field>
            <Field label="Voice detection method">
              <Select value={config.sync.vad} onChange={(event) => patch({ sync: { ...config.sync, vad: event.target.value } })}>
                <option value="">Default (webrtc)</option>
                <option value="webrtc">webrtc</option>
                <option value="auditok">auditok</option>
                <option value="silero">silero</option>
                <option value="fused">fused</option>
              </Select>
            </Field>
            <Field label="Audio sample length (seconds)" hint="Bounds ffmpeg extraction when a specific audio track is chosen.">
              <Input
                value={String(config.sync.audioReferenceSeconds)}
                inputMode="numeric"
                onChange={(event) => patch({ sync: { ...config.sync, audioReferenceSeconds: numberValue(event.target.value, config.sync.audioReferenceSeconds) } })}
              />
            </Field>
            <Field label="Highest embedded track number">
              <Input
                value={String(config.sync.maxEmbeddedStreamIndex)}
                inputMode="numeric"
                onChange={(event) =>
                  patch({ sync: { ...config.sync, maxEmbeddedStreamIndex: numberValue(event.target.value, config.sync.maxEmbeddedStreamIndex) } })
                }
              />
            </Field>
          </div>
        </Disclosure>

        <Disclosure label="AI translation" detail={sharedAI?.model || 'not configured'}>
          <div className="space-y-2 rounded-lg border border-border p-3">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="font-medium">Translation provider</span>
              <Badge variant={sharedAI?.model ? 'secondary' : 'outline'}>{sharedAI?.model ? 'ready' : 'not configured'}</Badge>
              {sharedAI?.model && <span className="text-xs text-muted-foreground">{sharedAI.model}</span>}
              {sharedAI?.apiKeyConfigured && <Badge variant="outline">API key stored</Badge>}
            </div>
            <p className="text-xs text-muted-foreground">
              Subtitle translation uses the same OpenAI-compatible service as recommendations. Configure the base URL, key,
              and model once in <a className="underline" href="#connections">Connections</a>.
            </p>
            {sharedAI?.baseURL && <p className="break-all text-xs text-muted-foreground">{sharedAI.baseURL}</p>}
            {sharedAIError && <ActionNote tone="warning">{sharedAIError}</ActionNote>}
          </div>
          <Toggle
            label="Allow subtitle translation"
            hint="Cue timing, language, and formatting are checked before anything is saved."
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
            <Field label="Max total tokens" hint="A translation stops when the budget is used up.">
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
        </Disclosure>
      </Section>
      </fieldset>
    </div>
  )
}
