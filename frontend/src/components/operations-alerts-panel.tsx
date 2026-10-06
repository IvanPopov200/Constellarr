import { useEffect, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { Activity, BellRing, CircleCheck, LoaderCircle, Save, Send, TriangleAlert } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Checkbox, EmptyState, ErrorNote, LoadingNote, Notice, Select } from '@/components/tv-ui'
import { useAuth } from '@/lib/auth-context'
import { errorMessage } from '@/lib/api'
import { formatAge } from '@/lib/format'
import { cn } from 'cn'
import {
  operationsApi,
  type AlertHistoryEntry,
  type AlertRule,
  type AlertRuleConfig,
  type AlertSeverity,
  type AlertsConfig,
  type WebhookUpdate,
} from '@/lib/operations-api'

const severities: AlertSeverity[] = ['info', 'warning', 'critical']

const severityTones: Record<AlertSeverity, string> = {
  info: 'border-sky-500/40 text-sky-300',
  warning: 'border-amber-500/40 text-amber-300',
  critical: 'border-destructive/40 bg-destructive/10 text-destructive',
}

const ruleCopy: Record<string, { title: string; description: string }> = {
  filesystem_low_space: {
    title: 'Low disk space',
    description: 'Fires when the data volume runs low on free space.',
  },
  provider_failures: {
    title: 'Provider failures',
    description: 'Fires when indexer, metadata, subtitle, or request providers fail repeatedly.',
  },
  download_import_failures: {
    title: 'Download and import failures',
    description: 'Fires when downloads fail or files cannot be imported.',
  },
  job_stuck: {
    title: 'Stuck jobs',
    description: 'Fires when an active download stops making progress.',
  },
  db_health: {
    title: 'Database health',
    description: 'Fires when PostgreSQL is unreachable.',
  },
}

function ruleTitle(name: string) {
  return ruleCopy[name]?.title ?? name.replace(/_/g, ' ')
}

function InfoNote({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="rounded-lg border border-border bg-muted/40 p-3 text-sm text-muted-foreground">
      {children}
    </p>
  )
}

const emptyWebhook: WebhookUpdate = {
  enabled: false,
  url: '',
  headerName: 'X-Constellarr-Secret',
  secret: '',
  timeoutSeconds: 5,
  minimumIntervalSeconds: 300,
  notifyRecovery: true,
}

// The saved view never carries the secret; the draft keeps the field empty until it is replaced.
function webhookDraft(view: AlertsConfig['webhook']): WebhookUpdate {
  return {
    enabled: view.enabled,
    url: view.url,
    headerName: view.headerName,
    secret: '',
    timeoutSeconds: view.timeoutSeconds,
    minimumIntervalSeconds: view.minimumIntervalSeconds,
    notifyRecovery: view.notifyRecovery,
  }
}

export function AlertsPanel() {
  const { can } = useAuth()
  const canReadSettings = can('settings.read')
  const canWriteSettings = can('settings.write')
  const [alerts, setAlerts] = useState<AlertRule[] | null>(null)
  const [config, setConfig] = useState<AlertsConfig | null>(null)
  const [history, setHistory] = useState<AlertHistoryEntry[]>([])
  const [draft, setDraft] = useState<AlertRuleConfig[] | null>(null)
  const [webhook, setWebhook] = useState<WebhookUpdate>(emptyWebhook)
  const [secretConfigured, setSecretConfigured] = useState(false)
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    // Monitoring readers may see live alerts without the settings permission that guards the thresholds.
    const settings = canReadSettings ? operationsApi.alertsConfig(controller.signal) : Promise.resolve(null)
    Promise.all([operationsApi.alerts(controller.signal), settings, operationsApi.alertHistory(controller.signal)])
      .then(([live, settings, entries]) => {
        if (controller.signal.aborted) return
        setAlerts(live.alerts)
        setHistory(entries)
        if (settings === null) return
        setConfig(settings)
        setDraft(settings.rules)
        setSecretConfigured(settings.webhook.secretConfigured)
        setWebhook(webhookDraft(settings.webhook))
      })
      .catch((cause) => {
        if (!controller.signal.aborted) setError(errorMessage(cause))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    const timer = window.setInterval(() => {
      operationsApi
        .alerts()
        .then((live) => setAlerts(live.alerts))
        .catch(() => undefined)
    }, 15000)
    return () => {
      window.clearInterval(timer)
      controller.abort()
    }
  }, [canReadSettings])

  const dirty =
    config !== null &&
    draft !== null &&
    (JSON.stringify(draft) !== JSON.stringify(config.rules) ||
      webhook.enabled !== config.webhook.enabled ||
      webhook.url !== config.webhook.url ||
      webhook.headerName !== config.webhook.headerName ||
      webhook.timeoutSeconds !== config.webhook.timeoutSeconds ||
      webhook.minimumIntervalSeconds !== config.webhook.minimumIntervalSeconds ||
      webhook.notifyRecovery !== config.webhook.notifyRecovery ||
      webhook.secret !== '')

  function updateRule(
    name: string,
    change: Omit<Partial<AlertRuleConfig>, 'thresholds'> & { thresholds?: Partial<AlertRuleConfig['thresholds']> },
  ) {
    if (!draft) return
    setNotice('')
    setError('')
    setDraft(
      draft.map((rule) =>
        rule.name === name ? { ...rule, ...change, thresholds: { ...rule.thresholds, ...(change.thresholds ?? {}) } } : rule,
      ),
    )
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (!draft || !canWriteSettings) return
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const saved = await operationsApi.saveAlertsConfig({ rules: draft, webhook })
      setConfig(saved)
      setDraft(saved.rules)
      setSecretConfigured(saved.webhook.secretConfigured)
      setWebhook(webhookDraft(saved.webhook))
      setNotice('Alert settings saved. Changes take effect on the next check.')
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  if (loading && !alerts) {
    return <LoadingNote>Loading alerts…</LoadingNote>
  }
  if (!alerts) {
    return <ErrorNote>{error || 'Alerts could not be loaded.'}</ErrorNote>
  }

  const firing = alerts.filter((alert) => alert.firing && alert.enabled)
  const editable = config !== null && draft !== null
  const readOnly = !canWriteSettings

  return (
    <form onSubmit={save} className="space-y-5">
      {error && <ErrorNote>{error}</ErrorNote>}
      {notice && <Notice>{notice}</Notice>}
      {!canReadSettings && (
        <InfoNote>
          Alert rules and notifications need the settings permission. Active alerts and history stay visible here.
        </InfoNote>
      )}
      {canReadSettings && !canWriteSettings && (
        <InfoNote>Alert settings are shown read-only. Changing them needs the settings write permission.</InfoNote>
      )}

      <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle className="flex items-center gap-2">
            <BellRing className="size-4 text-muted-foreground" />
            Active alerts
            <Badge variant={firing.length > 0 ? 'destructive' : 'outline'}>{firing.length}</Badge>
          </CardTitle>
          <CardDescription>Rules that currently need attention, refreshed automatically.</CardDescription>
        </CardHeader>
        <CardContent className="gap-3">
          {firing.length === 0 && <EmptyState>No alerts are firing. Rules below stay active in the background.</EmptyState>}
          <ul className="space-y-2">
            {firing.map((alert) => (
              <li key={alert.name} className={cn('rounded-lg border p-3', severityTones[alert.severity])} role="alert">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-sm font-medium">{ruleTitle(alert.name)}</span>
                  <Badge variant="outline" className={severityTones[alert.severity]}>
                    {alert.severity}
                  </Badge>
                </div>
                {alert.message && <p className="mt-1 text-sm">{alert.message}</p>}
                {alert.since && <p className="mt-1 text-xs opacity-80">Firing {formatAge(alert.since)}</p>}
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      {editable && (
        <details className="rounded-xl border border-border p-4">
          <summary className="cursor-pointer text-sm font-medium">Alert rules & notifications</summary>
        <fieldset disabled={readOnly} className="mt-4 space-y-5">
          <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle className="flex items-center gap-2">
            <Activity className="size-4 text-muted-foreground" />
            Alert rules
          </CardTitle>
          <CardDescription>Thresholds and severity for each built-in rule.</CardDescription>
        </CardHeader>
        <CardContent className="gap-4">
          {draft.map((rule) => (
            <div key={rule.name} className="space-y-3 rounded-lg border border-border p-3">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <p className="text-sm font-medium">{ruleTitle(rule.name)}</p>
                  <p className="text-xs text-muted-foreground">{ruleCopy[rule.name]?.description}</p>
                </div>
                <div className="flex items-center gap-3">
                  <Select
                    aria-label={`${ruleTitle(rule.name)} severity`}
                    value={rule.severity}
                    disabled={!rule.enabled}
                    onChange={(event) => updateRule(rule.name, { severity: event.target.value as AlertSeverity })}
                  >
                    {severities.map((severity) => (
                      <option key={severity} value={severity}>
                        {severity}
                      </option>
                    ))}
                  </Select>
                  <Checkbox
                    id={`rule-${rule.name}`}
                    label="Enabled"
                    checked={rule.enabled}
                    onChange={(enabled) => updateRule(rule.name, { enabled })}
                  />
                </div>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                {rule.name === 'filesystem_low_space' && (
                  <>
                    <NumberField
                      id={`${rule.name}-bytes`}
                      label="Minimum free space (GiB)"
                      value={rule.thresholds.minimumFreeBytes / 1024 ** 3}
                      step="0.5"
                      disabled={!rule.enabled}
                      onChange={(value) => updateRule(rule.name, { thresholds: { minimumFreeBytes: Math.round(value * 1024 ** 3) } })}
                    />
                    <NumberField
                      id={`${rule.name}-percent`}
                      label="Minimum free space (%)"
                      value={rule.thresholds.minimumFreePercent}
                      step="1"
                      disabled={!rule.enabled}
                      onChange={(value) => updateRule(rule.name, { thresholds: { minimumFreePercent: value } })}
                    />
                  </>
                )}
                {(rule.name === 'provider_failures' || rule.name === 'download_import_failures') && (
                  <>
                    <NumberField
                      id={`${rule.name}-window`}
                      label="Window (minutes)"
                      value={rule.thresholds.windowMinutes}
                      step="5"
                      disabled={!rule.enabled}
                      onChange={(value) => updateRule(rule.name, { thresholds: { windowMinutes: value } })}
                    />
                    <NumberField
                      id={`${rule.name}-threshold`}
                      label="Failures above"
                      value={rule.thresholds.threshold}
                      step="1"
                      disabled={!rule.enabled}
                      onChange={(value) => updateRule(rule.name, { thresholds: { threshold: value } })}
                    />
                  </>
                )}
                {rule.name === 'job_stuck' && (
                  <NumberField
                    id={`${rule.name}-stuck`}
                    label="No progress for (minutes)"
                    value={rule.thresholds.stuckMinutes}
                    step="5"
                    disabled={!rule.enabled}
                    onChange={(value) => updateRule(rule.name, { thresholds: { stuckMinutes: value } })}
                  />
                )}
                {rule.name === 'db_health' && (
                  <NumberField
                    id={`${rule.name}-failures`}
                    label="Failed checks in a row"
                    value={rule.thresholds.failures}
                    step="1"
                    disabled={!rule.enabled}
                    onChange={(value) => updateRule(rule.name, { thresholds: { failures: value } })}
                  />
                )}
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle className="flex items-center gap-2">
            <Send className="size-4 text-muted-foreground" />
            Webhook notifications
          </CardTitle>
          <CardDescription>
            Sends one JSON message per alert transition. Failures are logged without the URL or the secret.
          </CardDescription>
        </CardHeader>
        <CardContent className="gap-4">
          <Checkbox
            id="webhook-enabled"
            label="Send alert notifications"
            description="Recovery messages follow the switch below."
            checked={webhook.enabled}
            onChange={(enabled) => setWebhook({ ...webhook, enabled })}
          />
          <div className="space-y-2">
            <label htmlFor="webhook-url" className="text-sm font-medium">
              Webhook URL
            </label>
            <Input
              id="webhook-url"
              value={webhook.url}
              placeholder="https://example.com/constellarr"
              onChange={(event) => setWebhook({ ...webhook, url: event.target.value })}
            />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-2">
              <label htmlFor="webhook-header" className="text-sm font-medium">
                Secret header
              </label>
              <Input
                id="webhook-header"
                value={webhook.headerName}
                onChange={(event) => setWebhook({ ...webhook, headerName: event.target.value })}
              />
            </div>
            <div className="space-y-2">
              <label htmlFor="webhook-secret" className="text-sm font-medium">
                Secret value
              </label>
              <Input
                id="webhook-secret"
                type="password"
                autoComplete="new-password"
                value={webhook.secret ?? ''}
                placeholder={secretConfigured ? 'Saved secret (leave blank to keep)' : 'Optional shared secret'}
                onChange={(event) => setWebhook({ ...webhook, secret: event.target.value })}
              />
            </div>
            <NumberField
              id="webhook-timeout"
              label="Timeout (seconds)"
              value={webhook.timeoutSeconds}
              step="1"
              onChange={(value) => setWebhook({ ...webhook, timeoutSeconds: value })}
            />
            <NumberField
              id="webhook-interval"
              label="Minimum seconds between repeats"
              value={webhook.minimumIntervalSeconds}
              step="30"
              onChange={(value) => setWebhook({ ...webhook, minimumIntervalSeconds: value })}
            />
          </div>
          <Checkbox
            id="webhook-recovery"
            label="Send recovery notifications"
            description="A resolved message is delivered when an alert clears."
            checked={webhook.notifyRecovery}
            onChange={(notifyRecovery) => setWebhook({ ...webhook, notifyRecovery })}
          />
        </CardContent>
      </Card>
        </fieldset>
        </details>
      )}

      <Card className="shadow-none">
        <CardHeader className="border-b border-border">
          <CardTitle className="flex items-center gap-2">
            <CircleCheck className="size-4 text-muted-foreground" />
            Alert history
          </CardTitle>
          <CardDescription>The most recent rule transitions.</CardDescription>
        </CardHeader>
        <CardContent className="gap-2">
          {history.length === 0 && <EmptyState>No alerts have fired yet.</EmptyState>}
          <ul className="space-y-1 text-sm">
            {history.slice(0, 20).map((entry) => (
              <li key={entry.id} className="flex flex-wrap items-center gap-2 border-b border-border/50 pb-1 last:border-0">
                {entry.firing ? (
                  <TriangleAlert className={cn('size-3.5', severityTones[entry.severity])} />
                ) : (
                  <CircleCheck className="size-3.5 text-emerald-400" />
                )}
                <span className="font-medium">{ruleTitle(entry.name)}</span>
                <span className="text-muted-foreground">{entry.firing ? 'firing' : 'resolved'}</span>
                {entry.message && <span className="text-muted-foreground">{entry.message}</span>}
                <span className="ml-auto text-xs text-muted-foreground">{formatAge(entry.at)}</span>
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      {editable && canWriteSettings && (
        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-4">
          <p className="text-xs text-muted-foreground" role="status">
            {dirty ? 'You have unsaved alert settings.' : 'Alert settings are saved.'}
          </p>
          <div className="flex gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={!dirty || busy}
              onClick={() => {
                setDraft(config.rules)
                setWebhook(webhookDraft(config.webhook))
                setError('')
                setNotice('')
              }}
            >
              Discard
            </Button>
            <Button type="submit" size="sm" disabled={!dirty || busy}>
              {busy ? <LoaderCircle className="animate-spin motion-reduce:animate-none" /> : <Save />}
              Save alert settings
            </Button>
          </div>
        </div>
      )}
    </form>
  )
}

function NumberField({
  id,
  label,
  value,
  step,
  disabled,
  onChange,
}: {
  id: string
  label: string
  value: number
  step: string
  disabled?: boolean
  onChange: (value: number) => void
}) {
  return (
    <div className="space-y-2">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <Input
        id={id}
        type="number"
        min="0"
        step={step}
        className="max-w-32"
        disabled={disabled}
        value={Number.isFinite(value) ? value : 0}
        onChange={(event) => onChange(Number(event.target.value) || 0)}
      />
    </div>
  )
}
