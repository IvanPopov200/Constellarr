import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Check, ClipboardCopy, ExternalLink, Eye, EyeOff, LoaderCircle, Smartphone } from 'lucide-react'
import { PageHeading } from '@/components/app-shell'
import { Field, FormError } from '@/components/auth-gate'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Confirm, LoadError, LoadingRows } from '@/components/users-shared'
import { errorMessage } from '@/lib/api'
import { authApi, type ApiToken } from '@/lib/auth-api'
import { useAuth } from '@/lib/auth-context'
import {
  capabilityLabel,
  companionCapabilities,
  companionDeepLink,
  companionDeviceLabel,
  companionScopes,
  companionSetup,
  defaultServerURL,
  deviceTokenName,
  expiryDateISO,
  expiryLabel,
  isCompanionToken,
  isLoopbackServerURL,
  normalizeServerURL,
} from '@/lib/companion'
import { formatAge } from '@/lib/format'

type DeviceSetup = { id: string; name: string; token: string; serverURL: string }

const expiryChoices = [
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: '1 year' },
]

const selectClass =
  'h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/30'

function StepCard({ step, title, description, children }: {
  step: number
  title: string
  description: string
  children: ReactNode
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2.5">
          <Badge variant="secondary" className="size-5 justify-center rounded-full tabular-nums">{step}</Badge>
          {title}
        </CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent className="gap-4">{children}</CardContent>
    </Card>
  )
}

export function CompanionPage() {
  const { user, permissions, can } = useAuth()
  const [address, setAddress] = useState(() => defaultServerURL(window.location.origin, import.meta.env.BASE_URL))
  const [deviceName, setDeviceName] = useState('iPhone')
  const [expiryDays, setExpiryDays] = useState('90')
  const [allowDownloads, setAllowDownloads] = useState(() => can('downloads.write'))
  const [busy, setBusy] = useState(false)
  const [createError, setCreateError] = useState('')
  const [setup, setSetup] = useState<DeviceSetup | null>(null)
  const [secretVisible, setSecretVisible] = useState(false)
  const [copied, setCopied] = useState('')
  const [copyError, setCopyError] = useState('')
  const [tokens, setTokens] = useState<ApiToken[] | null>(null)
  const [listError, setListError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const setupRef = useRef<HTMLInputElement>(null)
  const tokenListRevision = useRef(0)

  const addressResult = normalizeServerURL(address)
  const loopbackAddress = 'url' in addressResult && isLoopbackServerURL(addressResult.url)
  const scopes = companionScopes(permissions, allowDownloads)
  const devices = tokens?.filter(token => isCompanionToken(token.name)) ?? []
  const setupURL = setup ? ('url' in addressResult ? addressResult.url : setup.serverURL) : ''
  const setupJSON = setup ? companionSetup(setupURL, setup.token) : ''
  const deepLink = setupURL ? companionDeepLink(setupURL) : ''

  useEffect(() => {
    const controller = new AbortController()
    let active = true
    const revision = tokenListRevision.current
    void authApi
      .listTokens(controller.signal)
      .then(items => {
        if (active && revision === tokenListRevision.current) {
          setTokens(items)
          setListError('')
        }
      })
      .catch(cause => {
        if (active && revision === tokenListRevision.current && !controller.signal.aborted) setListError(errorMessage(cause))
      })
    return () => {
      active = false
      controller.abort()
    }
  }, [attempt])

  useEffect(() => {
    if (setup) setupRef.current?.focus()
  }, [setup])

  async function createDevice(event: FormEvent) {
    event.preventDefault()
    if (!user || busy) return
    if (!('url' in addressResult)) {
      setCreateError(addressResult.error)
      return
    }
    if (!deviceName.trim()) {
      setCreateError('Name this device so you can recognize it later.')
      return
    }
    if (scopes.length === 0) {
      setCreateError('Your account has no permissions supported by the iOS app, so a device token cannot be created.')
      return
    }
    setBusy(true)
    setCreateError('')
    setCopyError('')
    setCopied('')
    try {
      // The API scopes token ownership to this account; permissions are always explicit.
      const created = await authApi.createToken({
        name: deviceTokenName(deviceName),
        permissions: scopes,
        expiresAt: expiryDateISO(Number(expiryDays)),
      })
      setSetup({ id: created.token.id, name: deviceName.trim(), token: created.secret, serverURL: addressResult.url })
      tokenListRevision.current += 1
      setTokens(list => [created.token, ...(list ?? []).filter(item => item.id !== created.token.id)])
      setAttempt(value => value + 1)
      setSecretVisible(false)
    } catch (cause) {
      setCreateError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  async function copy(label: string, value: string) {
    setCopyError('')
    try {
      if (!navigator.clipboard) throw new Error('The clipboard is unavailable.')
      await navigator.clipboard.writeText(value)
      setCopied(label)
    } catch {
      setCopied('')
      setCopyError('Copying was blocked. Select the value and copy it manually.')
    }
  }

  function hideSetup() {
    setSetup(null)
    setSecretVisible(false)
    setCopied('')
    setCopyError('')
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeading
        title="Connect iOS"
        description="Your companion server, within reach. Connect the Constellarr iOS app to your library and downloads."
      />

      <p className="max-w-3xl text-sm leading-relaxed text-muted-foreground">
        Browse movies and TV, follow downloads, and check requests and upcoming releases on your phone.
        Your companion keeps working when the app is closed.
      </p>

      <div className="grid items-start gap-6 lg:grid-cols-2">
      <StepCard
        step={1}
        title="Server address"
        description="Use the same server address you can open in Safari on your phone."
      >
        <Field
          label="Server address"
          htmlFor="companion-address"
          hint="Include the scheme and port, for example http://192.168.1.10:8080."
        >
          <Input
            id="companion-address"
            value={address}
            onChange={event => setAddress(event.target.value)}
            inputMode="url"
            autoComplete="off"
            spellCheck={false}
            aria-invalid={!('url' in addressResult)}
            aria-describedby={address.trim() !== '' && !('url' in addressResult) ? 'companion-address-help' : loopbackAddress ? 'companion-loopback-help' : undefined}
          />
        </Field>
        {address.trim() !== '' && !('url' in addressResult) && (
          <p id="companion-address-help" role="alert" className="text-sm text-destructive">
            {addressResult.error}
          </p>
        )}
        {loopbackAddress && (
          <p id="companion-loopback-help" role="status" className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-500">
            A phone cannot reach localhost; that address only works on this computer. Use the server's LAN address or a
            VPN hostname instead, for example http://192.168.1.10:8080.
          </p>
        )}
      </StepCard>

      <StepCard
        step={2}
        title="Device access"
        description="Name this device and choose what it can do."
      >
        <form onSubmit={createDevice} className="space-y-4">
          <FormError message={createError} />
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Device name" htmlFor="companion-device" hint="Shown in the device list below.">
              <Input
                id="companion-device"
                value={deviceName}
                onChange={event => setDeviceName(event.target.value)}
                maxLength={58}
                required
              />
            </Field>
            <Field label="Expiry" htmlFor="companion-expiry" hint="The token stops working after this period.">
              <select
                id="companion-expiry"
                className={selectClass}
                value={expiryDays}
                onChange={event => setExpiryDays(event.target.value)}
              >
                {expiryChoices.map(choice => (
                  <option key={choice.value} value={choice.value}>{choice.label}</option>
                ))}
              </select>
            </Field>
          </div>
          {can('downloads.write') && can('downloads.read') && (
            <label className="flex items-start gap-2.5 rounded-md p-1.5 hover:bg-muted/50">
              <input
                type="checkbox"
                className="mt-0.5 size-4 shrink-0 accent-[var(--primary)]"
                checked={allowDownloads}
                onChange={event => setAllowDownloads(event.target.checked)}
              />
              <span className="text-sm">
                Allow download controls
                <span className="block text-xs text-muted-foreground">
                  Pause and resume Usenet and torrent downloads from the phone.
                </span>
              </span>
            </label>
          )}
          <div className="rounded-lg border border-border p-3">
            <p className="text-sm font-medium">This device can</p>
            {scopes.length > 0 ? (
              <ul className="mt-2 space-y-1 text-sm text-muted-foreground">
                {companionCapabilities
                  .filter(capability => scopes.includes(capability.permission))
                  .map(capability => (
                    <li key={capability.permission} className="flex items-start gap-2">
                      <Check className="mt-0.5 size-3.5 shrink-0 text-primary" aria-hidden="true" />
                      {capability.label}
                    </li>
                  ))}
              </ul>
            ) : (
              <p className="mt-2 text-sm text-muted-foreground">
                Your account has no permissions supported by the iOS app, so a device token cannot be created
                here.
              </p>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Button
              type="submit"
              disabled={busy || !('url' in addressResult) || !deviceName.trim() || scopes.length === 0}
            >
              {busy ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : <Smartphone aria-hidden="true" />}
              {busy ? 'Creating device access…' : 'Create device access'}
            </Button>
            <span className="text-xs text-muted-foreground">
              The token is shown once, and Constellarr stores only its hash.
            </span>
          </div>
        </form>
      </StepCard>

      </div>

      <StepCard
        step={3}
        title="Connect the app"
        description={
          setup
            ? 'Paste this setup into the iPhone app while the phone is on the same network or VPN.'
            : 'Once step 2 creates the device access, its setup appears here one time, ready to paste.'
        }
      >
        {!setup && (
          <p className="text-sm text-muted-foreground">
            Create device access above, then copy the setup into Connect to your server → Paste connection in the iOS app.
          </p>
        )}
        {setup && (
          <div className="space-y-4">
            <FormError message={copyError} />
            {!('url' in addressResult) && (
              <p id="companion-loopback-help" role="status" className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-500">
                The address in step 1 is not valid right now, so this setup still uses {setup.serverURL}. Fix the
                address to update it.
              </p>
            )}
            <p className="text-sm">
              Setup for <span className="font-medium">{setup.name}</span> at{' '}
              <span className="font-mono text-xs break-all">{setupURL}</span>
            </p>
            <div className="space-y-1.5">
              <label htmlFor="companion-setup" className="text-sm font-medium">Setup</label>
              <div className="flex flex-col gap-2 sm:flex-row">
                <Input
                  id="companion-setup"
                  ref={setupRef}
                  readOnly
                  value={setupJSON}
                  className="font-mono text-xs"
                  onFocus={event => event.currentTarget.select()}
                />
                <Button type="button" variant="outline" onClick={() => void copy('setup', setupJSON)}>
                  <ClipboardCopy aria-hidden="true" />
                  {copied === 'setup' ? 'Copied' : 'Copy setup'}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">In the app, choose Paste connection and paste this text.</p>
            </div>
            <p id="companion-loopback-help" role="status" className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-500">
              This token is shown only now; Constellarr stores just its hash. If you lose it, revoke the device below
              and create a new one.
            </p>
            <div className="rounded-lg border border-border p-3">
              <p className="text-sm font-medium">Connect the app</p>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <Button asChild variant="outline">
                  <a href={deepLink}>
                    <ExternalLink aria-hidden="true" />
                    Open Constellarr for iOS
                  </a>
                </Button>
                <span className="text-xs text-muted-foreground">
                  Opens the app on this iPhone with the server address filled in; paste the setup or the token there to
                  finish.
                </span>
              </div>
              <div className="mt-3 grid gap-3">
                <div className="space-y-1.5">
                  <label htmlFor="companion-address-copy" className="text-xs font-medium text-muted-foreground">
                    Server address
                  </label>
                  <div className="flex flex-col gap-2 sm:flex-row">
                    <Input
                      id="companion-address-copy"
                      readOnly
                      value={setupURL}
                      className="font-mono text-xs"
                      onFocus={event => event.currentTarget.select()}
                    />
                    <Button type="button" variant="outline" onClick={() => void copy('address', setupURL)}>
                      <ClipboardCopy aria-hidden="true" />
                      {copied === 'address' ? 'Copied' : 'Copy address'}
                    </Button>
                  </div>
                </div>
                <div className="space-y-1.5">
                  <label htmlFor="companion-token" className="text-xs font-medium text-muted-foreground">Token</label>
                  <div className="flex flex-col gap-2 sm:flex-row">
                    <Input
                      id="companion-token"
                      readOnly
                      type={secretVisible ? 'text' : 'password'}
                      value={setup.token}
                      className="font-mono text-xs"
                      onFocus={event => event.currentTarget.select()}
                    />
                    <div className="flex gap-2">
                      <Button type="button" variant="outline" onClick={() => setSecretVisible(value => !value)}>
                        {secretVisible ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
                        {secretVisible ? 'Hide' : 'Show'}
                      </Button>
                      <Button type="button" variant="outline" onClick={() => void copy('token', setup.token)}>
                        <ClipboardCopy aria-hidden="true" />
                        {copied === 'token' ? 'Copied' : 'Copy token'}
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button type="button" variant="outline" onClick={hideSetup}>Hide setup</Button>
              <span className="text-xs text-muted-foreground">
                Hiding removes the token from this page; the device stays connected until you revoke it below.
              </span>
            </div>
          </div>
        )}
        <p role="status" className="sr-only">{copied ? `${copied} copied` : ''}</p>
      </StepCard>

      <p className="max-w-3xl text-sm text-muted-foreground">Library editing and server settings open in the web companion. Widgets, Live Activities, and playback currently use the iOS app’s direct-service connections.</p>

      <Card>
        <CardHeader>
          <CardTitle>iOS devices</CardTitle>
          <CardDescription>
            Manage access for your devices. Signed in as {user?.name}.
          </CardDescription>
        </CardHeader>
        <CardContent className="gap-3">
          {listError && (
            <LoadError
              message={listError}
              onRetry={() => {
                setListError('')
                setAttempt(value => value + 1)
              }}
            />
          )}
          {!tokens && !listError && <LoadingRows label="Loading devices…" />}
          {tokens && devices.length === 0 && (
            <p className="text-sm text-muted-foreground">
              No iOS devices yet. Create device access above to connect your first phone.
            </p>
          )}
          {devices.length > 0 && (
            <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border">
              {devices.map(token => {
                const label = companionDeviceLabel(token.name)
                return (
                  <li key={token.id} className="flex flex-wrap items-center gap-3 bg-background px-4 py-3">
                    <div className="min-w-0 flex-1">
                      <p className="truncate font-medium">{label}</p>
                      <p className="break-words text-xs text-muted-foreground">
                        {expiryLabel(token.expiresAt)} ·{' '}
                        {token.lastUsedAt ? `last used ${formatAge(token.lastUsedAt)}` : 'never used'}
                      </p>
                      <p className="break-words text-xs text-muted-foreground">
                        {token.permissions.length
                          ? token.permissions.map(capabilityLabel).join(' · ')
                          : 'No permissions'}
                      </p>
                    </div>
                    <Confirm
                      trigger={
                        <Button variant="ghost" size="sm" className="text-destructive" aria-label={`Revoke ${label}`}>
                          Revoke
                        </Button>
                      }
                      title="Revoke device access"
                      description={`Revoke ${label}? The app using it loses access immediately.`}
                      confirmLabel="Revoke token"
                      onConfirm={async () => {
                        await authApi.revokeToken(token.id)
                        tokenListRevision.current += 1
                        setAttempt(value => value + 1)
                        setTokens(list => list?.filter(item => item.id !== token.id) ?? null)
                        if (setup?.id === token.id) hideSetup()
                      }}
                    />
                  </li>
                )
              })}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
