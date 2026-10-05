import { useState, type FormEvent } from 'react'
import { Eye, EyeOff, LoaderCircle, LogIn } from 'lucide-react'
import { AuthCard, Field, FormError } from '@/components/auth-gate'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { useAuth } from '@/lib/auth-context'

export function AuthLogin({ notice = '' }: { notice?: string }) {
  const { login } = useAuth()
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [visible, setVisible] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!name.trim() || !password) {
      setError('Enter your name and password.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await login({ name: name.trim(), password })
    } catch (cause) {
      setError(errorMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthCard title="Sign in" description="Use your Constellarr account to continue.">
      <form onSubmit={submit} className="space-y-4">
        <FormError message={notice} />
        <FormError message={error} />
        <Field label="Name" htmlFor="login-name">
          <Input
            id="login-name"
            value={name}
            onChange={event => setName(event.target.value)}
            autoComplete="username"
            autoFocus
            required
          />
        </Field>
        <Field label="Password" htmlFor="login-password">
          <div className="relative">
            <Input
              id="login-password"
              type={visible ? 'text' : 'password'}
              value={password}
              onChange={event => setPassword(event.target.value)}
              autoComplete="current-password"
              className="pr-11"
              required
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              className="absolute top-0.5 right-1"
              aria-label={visible ? 'Hide password' : 'Show password'}
              aria-pressed={visible}
              onClick={() => setVisible(!visible)}
            >
              {visible ? <EyeOff /> : <Eye />}
            </Button>
          </div>
        </Field>
        <Button type="submit" className="w-full" disabled={busy}>
          {busy ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : <LogIn aria-hidden="true" />}
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
      </form>
    </AuthCard>
  )
}
