import { useCallback, useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import {
  ChevronDownIcon,
  GaugeIcon,
  LoaderCircleIcon,
  PauseIcon,
  PlayIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  api,
  errorMessage,
  formatSpeedLimit,
  kibPerSecondToMib,
  likelyTimeZone,
  mbpsToKibPerSecond,
  mibPerSecondToKib,
  type DownloadLimit,
  type DownloadLimitMode,
  type DownloadPolicyConfig,
  type DownloadPolicySnapshot,
  type DownloadWindow,
  type DownloadWindowAction,
} from '@/lib/api'
import { accessPermissions } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'

const pollIntervalMs = 5000

const limitModes: { value: DownloadLimitMode; label: string; hint: string }[] = [
  { value: 'unlimited', label: 'No speed limit', hint: 'Use the full available speed.' },
  { value: 'kbps', label: 'Fixed speed', hint: 'Cap every transfer at a speed you choose.' },
  {
    value: 'percent',
    label: 'Share of my connection',
    hint: 'A percentage of the connection speed you enter below.',
  },
]

const windowActions: { value: DownloadWindowAction; label: string }[] = [
  { value: 'full', label: 'Full speed' },
  { value: 'limited', label: 'Limited speed' },
  { value: 'paused', label: 'Paused (stays queued)' },
]

const weekdays = [
  { short: 'S', long: 'Sunday' },
  { short: 'M', long: 'Monday' },
  { short: 'T', long: 'Tuesday' },
  { short: 'W', long: 'Wednesday' },
  { short: 'T', long: 'Thursday' },
  { short: 'F', long: 'Friday' },
  { short: 'S', long: 'Saturday' },
]

const timezoneSuggestions = [
  'UTC',
  'Europe/London',
  'Europe/Berlin',
  'Europe/Madrid',
  'Europe/Warsaw',
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'Asia/Tokyo',
  'Australia/Sydney',
]

const timePattern = /^([01]\d|2[0-3]):[0-5]\d$/
const unlimited: DownloadLimit = { mode: 'unlimited', value: 0 }

type LimitFields = { speedMib: string; percent: string }

type EditorFields = { mbps: string; limit: LimitFields; windows: Record<string, LimitFields> }

let windowCounter = 0

function newWindowId() {
  windowCounter += 1
  return `window-${Date.now().toString(36)}-${windowCounter}`
}

function cloneConfig(config: DownloadPolicyConfig): DownloadPolicyConfig {
  return {
    ...config,
    limit: { ...config.limit },
    windows: config.windows.map((window) => ({
      ...window,
      days: [...window.days],
      limit: { ...window.limit },
    })),
  }
}

function limitFields(limit: DownloadLimit): LimitFields {
  return {
    speedMib:
      limit.mode === 'kbps' && limit.value > 0
        ? String(Number(kibPerSecondToMib(limit.value).toFixed(3)))
        : '2',
    percent: limit.mode === 'percent' && limit.value > 0 ? String(limit.value) : '50',
  }
}

function editorFields(config: DownloadPolicyConfig): EditorFields {
  return {
    mbps: config.connectionMbps > 0 ? String(config.connectionMbps) : '',
    limit: limitFields(config.limit),
    windows: Object.fromEntries(config.windows.map((window) => [window.id, limitFields(window.limit)])),
  }
}

function limitFrom(fields: LimitFields, mode: DownloadLimitMode): DownloadLimit {
  if (mode === 'kbps') return { mode, value: mibPerSecondToKib(Number(fields.speedMib) || 0) }
  if (mode === 'percent') return { mode, value: Math.round(Number(fields.percent) || 0) }
  return { mode: 'unlimited', value: 0 }
}

function configFrom(draft: DownloadPolicyConfig, fields: EditorFields): DownloadPolicyConfig {
  return {
    ...draft,
    timezone: draft.timezone.trim(),
    connectionMbps: Number(fields.mbps) || 0,
    limit: limitFrom(fields.limit, draft.limit.mode),
    windows: draft.windows.map((window) => ({
      ...window,
      name: window.name.trim(),
      days: [...window.days].sort((a, b) => a - b),
      limit: limitFrom(
        fields.windows[window.id] ?? limitFields(window.limit),
        window.action === 'limited'
          ? window.limit.mode === 'percent'
            ? 'percent'
            : 'kbps'
          : 'unlimited',
      ),
    })),
  }
}

function percentageCap(percent: number, mbps: number) {
  return (mbpsToKibPerSecond(mbps) * percent) / 100
}

function policyError(config: DownloadPolicyConfig) {
  if (config.limit.mode === 'percent') {
    if (!(config.connectionMbps > 0)) {
      return 'Enter your connection speed so the percentage limit can be calculated.'
    }
    if (!(config.limit.value >= 1 && config.limit.value <= 100)) {
      return 'Enter a share between 1 and 100 percent.'
    }
  }
  if (config.limit.mode === 'kbps' && !(config.limit.value > 0)) {
    return 'Enter a speed above zero, or choose no speed limit.'
  }
  if (!config.scheduleEnabled) return null
  if (!likelyTimeZone(config.timezone)) return 'Enter a timezone such as Europe/Berlin.'
  for (const [index, window] of config.windows.entries()) {
    const label = window.name.trim() || `Window ${index + 1}`
    if (window.days.length === 0) return `${label}: pick at least one day.`
    if (!timePattern.test(window.start) || !timePattern.test(window.end)) {
      return `${label}: enter a start and end time.`
    }
    if (window.action === 'limited' && !(window.limit.value > 0)) {
      return `${label}: enter a speed above zero.`
    }
    if (window.action === 'limited' && window.limit.mode === 'percent' && !(config.connectionMbps > 0)) {
      return `${label}: enter your connection speed so the percentage limit can be calculated.`
    }
  }
  return null
}

function newWindow(): DownloadWindow {
  return {
    id: newWindowId(),
    name: 'New window',
    days: [1, 2, 3, 4, 5],
    start: '09:00',
    end: '17:00',
    action: 'limited',
    limit: { mode: 'kbps', value: mibPerSecondToKib(1) },
  }
}

function timingHint(window: DownloadWindow) {
  if (window.start === window.end) return 'Runs all 24 hours on the selected days.'
  if (window.end < window.start) return `Runs overnight, from ${window.start} until ${window.end} the next day.`
  return null
}

function nextChangeLabel(value: string) {
  const at = new Date(value)
  if (Number.isNaN(at.getTime())) return value
  const clock = at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const minutes = Math.round((at.getTime() - Date.now()) / 60_000)
  if (minutes <= 0) return `at ${clock}`
  if (minutes < 60) return `in ${minutes} min (${clock})`
  const hours = Math.round(minutes / 60)
  return hours < 24 ? `in ${hours} h (${clock})` : `in ${Math.round(hours / 24)} d (${clock})`
}

function dateTimeTitle(value: string) {
  const at = new Date(value)
  return Number.isNaN(at.getTime()) ? value : at.toLocaleString()
}

const deviceTimezone = (() => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    return ''
  }
})()

export function DownloadControls() {
  const { can } = useAuth()
  const canRead = can(accessPermissions.downloadsRead)
  const canControl = can(accessPermissions.downloadsWrite)
  const canEdit = can(accessPermissions.settingsWrite)
  const canViewPolicy = can(accessPermissions.settingsRead) || canEdit

  const [snapshot, setSnapshot] = useState<DownloadPolicySnapshot | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<DownloadPolicyConfig | null>(null)
  const [fields, setFields] = useState<EditorFields>(() => ({
    mbps: '',
    limit: { speedMib: '2', percent: '50' },
    windows: {},
  }))
  const [dirty, setDirty] = useState(false)
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [controlling, setControlling] = useState(false)
  const [controlError, setControlError] = useState<string | null>(null)
  const dirtyRef = useRef(false)
  const openRef = useRef(false)

  const applySnapshot = useCallback((next: DownloadPolicySnapshot) => {
    setSnapshot(next)
    if (dirtyRef.current) return
    setDraft((current) => (current === null ? null : cloneConfig(next.config)))
    if (openRef.current) setFields(editorFields(next.config))
  }, [])

  useEffect(() => {
    if (!canRead) return
    const controller = new AbortController()
    let stopped = false
    const tick = async () => {
      try {
        const next = await api.getDownloadPolicy(controller.signal)
        if (stopped) return
        applySnapshot(next)
        setLoadError(null)
      } catch (cause) {
        if (stopped || controller.signal.aborted) return
        setLoadError(errorMessage(cause))
      }
    }
    void tick()
    const timer = window.setInterval(() => void tick(), pollIntervalMs)
    return () => {
      stopped = true
      controller.abort()
      window.clearInterval(timer)
    }
  }, [canRead, applySnapshot, reloadKey])

  const change = (next: DownloadPolicyConfig) => {
    setDraft(next)
    dirtyRef.current = true
    setDirty(true)
    setNotice(null)
  }

  const openEditor = () => {
    if (!snapshot) return
    setDraft(cloneConfig(snapshot.config))
    setFields(editorFields(snapshot.config))
    dirtyRef.current = false
    openRef.current = true
    setDirty(false)
    setConfirmDiscard(false)
    setSaveError(null)
    setNotice(null)
    setOpen(true)
  }

  const closeEditor = () => {
    setOpen(false)
    setDraft(null)
    dirtyRef.current = false
    openRef.current = false
    setDirty(false)
    setConfirmDiscard(false)
    setSaveError(null)
  }

  const requestClose = () => {
    if (dirty) {
      setConfirmDiscard(true)
      return
    }
    closeEditor()
  }

  const togglePaused = async () => {
    if (!snapshot) return
    setControlling(true)
    setControlError(null)
    try {
      const next = snapshot.config.paused ? await api.resumeAllDownloads() : await api.pauseAllDownloads()
      applySnapshot(next)
    } catch (cause) {
      setControlError(errorMessage(cause))
    } finally {
      setControlling(false)
    }
  }

  const save = async (event: FormEvent) => {
    event.preventDefault()
    if (!draft) return
    const candidate = configFrom(draft, fields)
    const invalid = policyError(candidate)
    if (invalid) {
      setSaveError(invalid)
      return
    }
    setSaving(true)
    setSaveError(null)
    setNotice(null)
    try {
      const saved = await api.saveDownloadPolicy(candidate)
      setSnapshot(saved)
      setDraft(cloneConfig(saved.config))
      setFields(editorFields(saved.config))
      dirtyRef.current = false
      setDirty(false)
      setNotice('Saved. The new speed cap and schedule apply within a few seconds.')
    } catch (cause) {
      setSaveError(errorMessage(cause))
    } finally {
      setSaving(false)
    }
  }

  const updateWindow = (id: string, patch: Partial<DownloadWindow>) => {
    if (!draft) return
    change({ ...draft, windows: draft.windows.map((window) => (window.id === id ? { ...window, ...patch } : window)) })
  }

  const updateWindowFields = (id: string, patch: Partial<LimitFields>) => {
    setFields((current) => ({
      ...current,
      windows: {
        ...current.windows,
        [id]: { ...(current.windows[id] ?? { speedMib: '1', percent: '50' }), ...patch },
      },
    }))
  }

  const addWindow = () => {
    if (!draft) return
    const next = newWindow()
    setFields((current) => ({ ...current, windows: { ...current.windows, [next.id]: limitFields(next.limit) } }))
    change({ ...draft, windows: [...draft.windows, next] })
  }

  const applyPreset = (windows: DownloadWindow[], outsideSchedule: 'normal' | 'paused') => {
    if (!draft) return
    setFields((current) => ({
      ...current,
      windows: Object.fromEntries(windows.map((window) => [window.id, limitFields(window.limit)])),
    }))
    change({ ...draft, scheduleEnabled: true, outsideSchedule, windows })
  }

  if (!canRead) return null

  const effective = snapshot?.effective
  const config = snapshot?.config
  const paused = effective?.paused ?? false
  const limited = !paused && (effective?.limitBytesPerSecond ?? 0) > 0

  const stateSummary = () => {
    if (!snapshot) return ''
    if (snapshot.effective.paused) return snapshot.effective.reason || 'Downloads are paused.'
    if (snapshot.effective.limitBytesPerSecond <= 0) {
      return 'No speed cap; downloads use the full available speed.'
    }
    const share =
      snapshot.config.limit.mode === 'percent' && snapshot.config.connectionMbps > 0
        ? ` (${snapshot.config.limit.value}% of ${snapshot.config.connectionMbps} Mbps)`
        : ''
    return `Speed capped at ${formatSpeedLimit(snapshot.effective.limitBytesPerSecond / 1024)}${share}.`
  }

  const basicCapPreview = () => {
    if (!draft) return null
    if (draft.limit.mode === 'kbps') {
      const kib = mibPerSecondToKib(Number(fields.limit.speedMib) || 0)
      return kib > 0 ? `Cap ≈ ${formatSpeedLimit(kib)}` : 'Enter a speed above zero.'
    }
    if (draft.limit.mode === 'percent') {
      const mbps = Number(fields.mbps) || 0
      if (!(mbps > 0)) return 'Enter your connection speed to see the cap.'
      const percent = Number(fields.limit.percent) || 0
      if (!(percent > 0)) return null
      return `Cap ≈ ${formatSpeedLimit(percentageCap(percent, mbps))} while this limit applies.`
    }
    return null
  }

  const windowCapPreview = (window: DownloadWindow) => {
    if (!draft || window.action !== 'limited') return null
    const entry = fields.windows[window.id] ?? limitFields(window.limit)
    if (window.limit.mode === 'percent') {
      const mbps = Number(fields.mbps) || 0
      if (!(mbps > 0)) return 'Enter your connection speed to see the cap.'
      const percent = Number(entry.percent) || 0
      if (!(percent > 0)) return null
      return `Cap ≈ ${formatSpeedLimit(percentageCap(percent, mbps))}`
    }
    const kib = mibPerSecondToKib(Number(entry.speedMib) || 0)
    return kib > 0 ? `Cap ≈ ${formatSpeedLimit(kib)}` : 'Enter a speed above zero.'
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          Download controls
        </CardTitle>
        <CardDescription>
          Pause every transfer, cap the speed, and choose when downloads run. The same rules apply to
          Usenet and torrents.
        </CardDescription>
        {canControl && (
          <CardAction>
            <Button
              size="sm"
              variant={config?.paused ? 'default' : 'outline'}
              disabled={!snapshot || controlling}
              onClick={() => void togglePaused()}
            >
              {controlling ? (
                <LoaderCircleIcon
                  data-icon="inline-start"
                  aria-hidden="true"
                  className="animate-spin motion-reduce:animate-none"
                />
              ) : config?.paused ? (
                <PlayIcon data-icon="inline-start" aria-hidden="true" />
              ) : (
                <PauseIcon data-icon="inline-start" aria-hidden="true" />
              )}
              {config?.paused ? 'Resume all' : 'Pause all'}
            </Button>
          </CardAction>
        )}
      </CardHeader>

      <CardContent>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border pb-3">
          {snapshot === null ? (
            <span className="flex items-center gap-2 text-sm text-muted-foreground">
              <LoaderCircleIcon
                aria-hidden="true"
                className="size-4 animate-spin motion-reduce:animate-none"
              />
              Loading the current download state…
            </span>
          ) : (
            <>
              <Badge
                variant="outline"
                className={paused ? 'border-amber-500/25 bg-amber-500/10 text-amber-400' : undefined}
              >
                {paused ? <PauseIcon aria-hidden="true" /> : <GaugeIcon aria-hidden="true" />}
                {paused ? 'Paused' : limited ? 'Limited' : 'Full speed'}
              </Badge>
              <span className="text-sm text-muted-foreground">{stateSummary()}</span>
              {effective?.nextChange && (
                <span className="text-xs text-muted-foreground">
                  Next change{' '}
                  <time dateTime={effective.nextChange} title={dateTimeTitle(effective.nextChange)}>
                    {nextChangeLabel(effective.nextChange)}
                  </time>
                </span>
              )}
            </>
          )}
        </div>

        {loadError && (
          <div
            role="alert"
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            <span>Could not load the download state. {loadError}</span>
            <Button size="sm" variant="outline" onClick={() => setReloadKey((current) => current + 1)}>
              <RefreshCwIcon data-icon="inline-start" aria-hidden="true" />
              Retry
            </Button>
          </div>
        )}

        {controlError && (
          <p role="alert" className="text-sm text-destructive">
            {controlError}
          </p>
        )}

        {canViewPolicy && (
          <div className="flex flex-col gap-3">
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-expanded={open}
                aria-controls="download-policy-editor"
                disabled={!snapshot}
                onClick={() => (open ? requestClose() : openEditor())}
              >
                <GaugeIcon data-icon="inline-start" aria-hidden="true" />
                Speed &amp; schedule
                <ChevronDownIcon
                  aria-hidden="true"
                  className={
                    open
                      ? 'rotate-180 transition-transform motion-reduce:transition-none'
                      : 'transition-transform motion-reduce:transition-none'
                  }
                />
              </Button>
            </div>

            {confirmDiscard && (
              <div
                role="alert"
                className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
              >
                <span>You have unsaved changes.</span>
                <Button size="sm" variant="destructive" onClick={closeEditor}>
                  Discard changes
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setConfirmDiscard(false)}>
                  Keep editing
                </Button>
              </div>
            )}

            {open && draft && (
              <form
                id="download-policy-editor"
                className="flex flex-col gap-6 rounded-lg border border-border px-3 py-4 sm:px-4"
                onSubmit={save}
              >
                <fieldset disabled={!canEdit} className="flex flex-col gap-6">
                  <fieldset className="flex flex-col gap-2">
                    <legend className="text-sm font-medium">Speed cap</legend>
                    <p className="text-xs text-muted-foreground">
                      Applies whenever the schedule below is not limiting downloads.
                    </p>
                    <div className="flex flex-col gap-1.5">
                      {limitModes.map((mode) => (
                        <label key={mode.value} className="flex items-start gap-2 text-sm">
                          <input
                            type="radio"
                            name="download-limit-mode"
                            className="mt-1"
                            value={mode.value}
                            checked={draft.limit.mode === mode.value}
                            onChange={() => change({ ...draft, limit: { ...draft.limit, mode: mode.value } })}
                          />
                          <span className="flex flex-col">
                            {mode.label}
                            <span className="text-xs text-muted-foreground">{mode.hint}</span>
                          </span>
                        </label>
                      ))}
                    </div>

                    {draft.limit.mode === 'kbps' && (
                      <div className="flex flex-wrap items-end gap-3">
                        <label className="flex flex-col gap-1 text-sm">
                          Speed cap (MiB/s)
                          <Input
                            className="w-28"
                            inputMode="decimal"
                            value={fields.limit.speedMib}
                            onChange={(event) =>
                              setFields((current) => ({
                                ...current,
                                limit: { ...current.limit, speedMib: event.target.value },
                              }))
                            }
                          />
                        </label>
                        <p className="pb-2 text-xs text-muted-foreground">{basicCapPreview()}</p>
                      </div>
                    )}

                    {draft.limit.mode === 'percent' && (
                      <div className="flex flex-wrap items-end gap-3">
                        <label className="flex flex-col gap-1 text-sm">
                          Your connection speed (Mbps)
                          <Input
                            className="w-28"
                            inputMode="decimal"
                            value={fields.mbps}
                            onChange={(event) =>
                              setFields((current) => ({ ...current, mbps: event.target.value }))
                            }
                          />
                        </label>
                        <label className="flex flex-col gap-1 text-sm">
                          Share of connection (%)
                          <Input
                            className="w-24"
                            inputMode="numeric"
                            value={fields.limit.percent}
                            onChange={(event) =>
                              setFields((current) => ({
                                ...current,
                                limit: { ...current.limit, percent: event.target.value },
                              }))
                            }
                          />
                        </label>
                        <p className="pb-2 text-xs text-muted-foreground">{basicCapPreview()}</p>
                      </div>
                    )}

                    {draft.limit.mode === 'percent' && (
                      <p className="text-xs text-muted-foreground">
                        Enter the speed you expect from your line; Constellarr does not measure it.
                      </p>
                    )}
                  </fieldset>

                  <fieldset className="flex flex-col gap-3">
                    <legend className="text-sm font-medium">Schedule</legend>
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={draft.scheduleEnabled}
                        onChange={(event) =>
                          change({
                            ...draft,
                            scheduleEnabled: event.target.checked,
                            timezone: event.target.checked && !draft.timezone ? deviceTimezone : draft.timezone,
                            windows:
                              event.target.checked && draft.windows.length === 0 ? [newWindow()] : draft.windows,
                          })
                        }
                      />
                      Limit downloads on a weekly schedule
                    </label>

                    {draft.scheduleEnabled && (
                      <>
                        <div className="flex flex-wrap items-end gap-3">
                          <label className="flex flex-col gap-1 text-sm">
                            Timezone
                            <Input
                              className="w-56"
                              list="download-policy-timezones"
                              value={draft.timezone}
                              placeholder="Europe/Berlin"
                              spellCheck={false}
                              onChange={(event) => change({ ...draft, timezone: event.target.value })}
                            />
                          </label>
                          <datalist id="download-policy-timezones">
                            {timezoneSuggestions.map((zone) => (
                              <option key={zone} value={zone} />
                            ))}
                          </datalist>
                          {deviceTimezone && (
                            <Button
                              type="button"
                              size="sm"
                              variant="ghost"
                              onClick={() => change({ ...draft, timezone: deviceTimezone })}
                            >
                              Use this device’s timezone ({deviceTimezone})
                            </Button>
                          )}
                        </div>

                        <fieldset className="flex flex-col gap-1.5">
                          <legend className="text-xs text-muted-foreground">
                            Outside these windows
                          </legend>
                          <label className="flex items-center gap-2 text-sm">
                            <input
                              type="radio"
                              name="download-outside-schedule"
                              checked={draft.outsideSchedule === 'normal'}
                              onChange={() => change({ ...draft, outsideSchedule: 'normal' })}
                            />
                            Keep downloading normally (still subject to the speed cap)
                          </label>
                          <label className="flex items-center gap-2 text-sm">
                            <input
                              type="radio"
                              name="download-outside-schedule"
                              checked={draft.outsideSchedule === 'paused'}
                              onChange={() => change({ ...draft, outsideSchedule: 'paused' })}
                            />
                            Pause downloads; they stay queued until the next window
                          </label>
                        </fieldset>

                        <div className="flex flex-wrap gap-2">
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            onClick={() => applyPreset(
                              [
                                {
                                  id: newWindowId(),
                                  name: 'Overnight full speed',
                                  days: [0, 1, 2, 3, 4, 5, 6],
                                  start: '22:00',
                                  end: '07:00',
                                  action: 'full',
                                  limit: { ...unlimited },
                                },
                              ],
                              'paused',
                            )}
                          >
                            Preset: full speed overnight 22:00–07:00, queued during the day
                          </Button>
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            onClick={() => applyPreset(
                              [
                                {
                                  id: newWindowId(),
                                  name: 'Quiet hours',
                                  days: [0, 1, 2, 3, 4, 5, 6],
                                  start: '23:00',
                                  end: '07:00',
                                  action: 'paused',
                                  limit: { ...unlimited },
                                },
                              ],
                              'normal',
                            )}
                          >
                            Preset: pause overnight 23:00–07:00
                          </Button>
                        </div>

                        {draft.windows.length === 0 && (
                          <p className="text-xs text-muted-foreground">
                            Add a window to say when downloads may run.
                          </p>
                        )}

                        {draft.windows.map((window, index) => {
                          const hours = timingHint(window)
                          const label = window.name.trim() || `Window ${index + 1}`
                          return (
                            <div
                              key={window.id}
                              className="flex flex-col gap-3 rounded-lg border border-border bg-background/40 px-3 py-3"
                            >
                              <div className="flex flex-wrap items-end gap-2">
                                <label className="flex min-w-40 flex-1 flex-col gap-1 text-sm">
                                  Name
                                  <Input
                                    value={window.name}
                                    placeholder={`Window ${index + 1}`}
                                    onChange={(event) => updateWindow(window.id, { name: event.target.value })}
                                  />
                                </label>
                                <Button
                                  type="button"
                                  size="sm"
                                  variant="ghost"
                                  disabled={!canEdit}
                                  aria-label={`Remove ${label}`}
                                  onClick={() =>
                                    change({
                                      ...draft,
                                      windows: draft.windows.filter((item) => item.id !== window.id),
                                    })
                                  }
                                >
                                  <Trash2Icon aria-hidden="true" />
                                  Remove
                                </Button>
                              </div>

                              <fieldset className="flex flex-col gap-1">
                                <legend className="text-xs text-muted-foreground">Days</legend>
                                <div className="flex flex-wrap gap-1">
                                  {weekdays.map((day, dayIndex) => {
                                    const selected = window.days.includes(dayIndex)
                                    return (
                                      <Button
                                        key={day.long}
                                        type="button"
                                        size="xs"
                                        variant={selected ? 'secondary' : 'outline'}
                                        aria-pressed={selected}
                                        aria-label={day.long}
                                        onClick={() =>
                                          updateWindow(window.id, {
                                            days: selected
                                              ? window.days.filter((value) => value !== dayIndex)
                                              : [...window.days, dayIndex].sort((a, b) => a - b),
                                          })
                                        }
                                      >
                                        {day.short}
                                      </Button>
                                    )
                                  })}
                                </div>
                              </fieldset>

                              <div className="flex flex-wrap items-end gap-3">
                                <label className="flex flex-col gap-1 text-sm">
                                  From
                                  <Input
                                    type="time"
                                    className="w-28"
                                    value={window.start}
                                    onChange={(event) => updateWindow(window.id, { start: event.target.value })}
                                  />
                                </label>
                                <label className="flex flex-col gap-1 text-sm">
                                  To
                                  <Input
                                    type="time"
                                    className="w-28"
                                    value={window.end}
                                    onChange={(event) => updateWindow(window.id, { end: event.target.value })}
                                  />
                                </label>
                                <label className="flex flex-col gap-1 text-sm">
                                  During this window
                                  <select
                                    className="h-9 rounded-md border border-input bg-transparent px-2 text-sm shadow-xs dark:bg-input/30"
                                    value={window.action}
                                    onChange={(event) =>
                                      updateWindow(window.id, {
                                        action: event.target.value as DownloadWindowAction,
                                        limit:
                                          event.target.value === 'limited' && window.limit.mode === 'unlimited'
                                            ? { mode: 'kbps', value: mibPerSecondToKib(1) }
                                            : window.limit,
                                      })
                                    }
                                  >
                                    {windowActions.map((action) => (
                                      <option key={action.value} value={action.value}>
                                        {action.label}
                                      </option>
                                    ))}
                                  </select>
                                </label>
                              </div>

                              {window.action === 'limited' && (
                                <div className="flex flex-wrap items-end gap-3">
                                  <label className="flex flex-col gap-1 text-sm">
                                    Limit type
                                    <select
                                      className="h-9 rounded-md border border-input bg-transparent px-2 text-sm shadow-xs dark:bg-input/30"
                                      value={window.limit.mode === 'percent' ? 'percent' : 'kbps'}
                                      onChange={(event) =>
                                        updateWindow(window.id, {
                                          limit: {
                                            ...window.limit,
                                            mode: event.target.value as DownloadLimitMode,
                                          },
                                        })
                                      }
                                    >
                                      <option value="kbps">Fixed speed</option>
                                      <option value="percent">Share of connection</option>
                                    </select>
                                  </label>
                                  {window.limit.mode === 'percent' ? (
                                    <label className="flex flex-col gap-1 text-sm">
                                      Share (%)
                                      <Input
                                        className="w-24"
                                        inputMode="numeric"
                                        value={
                                          (fields.windows[window.id] ?? limitFields(window.limit)).percent
                                        }
                                        onChange={(event) =>
                                          updateWindowFields(window.id, { percent: event.target.value })
                                        }
                                      />
                                    </label>
                                  ) : (
                                    <label className="flex flex-col gap-1 text-sm">
                                      Speed (MiB/s)
                                      <Input
                                        className="w-28"
                                        inputMode="decimal"
                                        value={
                                          (fields.windows[window.id] ?? limitFields(window.limit)).speedMib
                                        }
                                        onChange={(event) =>
                                          updateWindowFields(window.id, { speedMib: event.target.value })
                                        }
                                      />
                                    </label>
                                  )}
                                  <p className="pb-2 text-xs text-muted-foreground">
                                    {windowCapPreview(window)}
                                  </p>
                                </div>
                              )}

                              {hours && <p className="text-xs text-muted-foreground">{hours}</p>}
                            </div>
                          )
                        })}

                        <div>
                          <Button type="button" size="sm" variant="outline" disabled={!canEdit} onClick={addWindow}>
                            <PlusIcon data-icon="inline-start" aria-hidden="true" />
                            Add window
                          </Button>
                        </div>
                      </>
                    )}
                  </fieldset>
                </fieldset>

                <div className="flex flex-wrap items-center gap-3 border-t border-border pt-3">
                  {canEdit ? (
                    <Button type="submit" disabled={saving || !dirty}>
                      {saving && (
                        <LoaderCircleIcon
                          data-icon="inline-start"
                          aria-hidden="true"
                          className="animate-spin motion-reduce:animate-none"
                        />
                      )}
                      {saving ? 'Saving…' : 'Save changes'}
                    </Button>
                  ) : null}
                  <Button type="button" variant="ghost" onClick={requestClose}>
                    Close
                  </Button>
                  {dirty && <span className="text-xs text-amber-400">Unsaved changes</span>}
                  {!dirty && notice && (
                    <span role="status" className="text-xs text-muted-foreground">
                      {notice}
                    </span>
                  )}
                </div>
                {saveError && (
                  <p role="alert" className="text-sm text-destructive">
                    {saveError}
                  </p>
                )}
              </form>
            )}

            {open && !canEdit && (
              <p className="text-xs text-muted-foreground">
                Your role can view these settings but not change them.
              </p>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
