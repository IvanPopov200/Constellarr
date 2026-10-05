import { useState, type FormEvent } from 'react'
import { LoaderCircle, ShieldCheck } from 'lucide-react'
import { AuthCard, Field, FormError } from '@/components/auth-gate'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { useAuth } from '@/lib/auth-context'

export function AuthSetup() {
  const { completeSetup } = useAuth()
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (name.trim().length < 2) {
      setError('Choose a name of at least 2 characters.')
      return
    }
    if (password.length < 8) {
      setError('Use a password of at least 8 characters.')
      return
    }
    if (password !== confirm) {
      setError('The passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await completeSetup({ name: name.trim(), password })
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthCard title="Welcome to Constellarr" description="Create the first administrator account for this server.">
      <form onSubmit={submit} className="space-y-4">
        <FormError message={error} />
        <Field label="Administrator name" htmlFor="setup-name" hint="Used to sign in and shown beside your activity.">
          <Input
            id="setup-name"
            value={name}
            onChange={event => setName(event.target.value)}
            autoComplete="username"
            autoFocus
            required
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Password" htmlFor="setup-password" hint="At least 8 characters.">
            <Input
              id="setup-password"
              type="password"
              value={password}
              onChange={event => setPassword(event.target.value)}
              autoComplete="new-password"
              required
            />
          </Field>
          <Field label="Confirm password" htmlFor="setup-confirm">
            <Input
              id="setup-confirm"
              type="password"
              value={confirm}
              onChange={event => setConfirm(event.target.value)}
              autoComplete="new-password"
              required
            />
          </Field>
        </div>
        <p className="flex items-start gap-2 rounded-lg border border-border p-3 text-xs text-muted-foreground">
          <ShieldCheck className="mt-0.5 size-4 shrink-0 text-primary" aria-hidden="true" />
          This account owns user management, roles, and server settings. You can create more accounts afterwards.
        </p>
        <Button type="submit" className="w-full" disabled={busy}>
          {busy && <LoaderCircle className="animate-spin" aria-hidden="true" />}
          {busy ? 'Creating account…' : 'Create administrator account'}
        </Button>
      </form>
    </AuthCard>
  )
}
