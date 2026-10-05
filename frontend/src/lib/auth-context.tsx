import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { errorMessage } from '@/lib/api'
import {
  authApi,
  clearAuthCache,
  hasPermission,
  UnauthorizedError,
  type AuthUser,
  type LoginInput,
  type Session,
  type SetupInput,
} from '@/lib/auth-api'

export type AuthStatus = 'loading' | 'setup' | 'anonymous' | 'authenticated' | 'error'

type AuthState = {
  status: AuthStatus
  user: AuthUser | null
  permissions: string[]
  error: string
}

export type AuthContextValue = {
  status: AuthStatus
  user: AuthUser | null
  permissions: string[]
  error: string
  can: (permission: string | string[], mode?: 'all' | 'any') => boolean
  login: (input: LoginInput) => Promise<void>
  logout: (message?: string) => Promise<void>
  refresh: () => Promise<void>
  retry: () => void
  completeSetup: (input: SetupInput) => Promise<void>
}

const signedOut: AuthState = { status: 'anonymous', user: null, permissions: [], error: '' }

const AuthContext = createContext<AuthContextValue | null>(null)

function sessionState(session: Session): AuthState {
  return { status: 'authenticated', user: session.user, permissions: session.permissions, error: '' }
}

function readable(cause: unknown) {
  return errorMessage(cause)
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({ ...signedOut, status: 'loading' })
  const [attempt, setAttempt] = useState(0)

  const clearSession = useCallback((error: string) => {
    clearAuthCache()
    setState({ ...signedOut, error })
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    let active = true
    void authApi
      .status(controller.signal)
      .then(result => {
        if (!active) return
        if (result.session) setState(sessionState(result.session))
        else if (result.setupRequired) setState({ ...signedOut, status: 'setup' })
        else setState(signedOut)
      })
      .catch(cause => {
        if (!active || controller.signal.aborted) return
        if (cause instanceof UnauthorizedError) setState(signedOut)
        else setState({ ...signedOut, status: 'error', error: readable(cause) })
      })
    return () => {
      active = false
      controller.abort()
    }
  }, [attempt])

  useEffect(() => {
    // The shared fetch wrapper dispatches this when the server rejects a session.
    function onUnauthorized(event: Event) {
      const detail = event instanceof CustomEvent && typeof event.detail === 'string' ? event.detail : ''
      clearSession(detail || 'Your session expired. Sign in again.')
    }
    window.addEventListener('constellarr:unauthorized', onUnauthorized)
    return () => window.removeEventListener('constellarr:unauthorized', onUnauthorized)
  }, [clearSession])

  const login = useCallback(async (input: LoginInput) => {
    setState(sessionState(await authApi.login(input)))
  }, [])

  const completeSetup = useCallback(async (input: SetupInput) => {
    setState(sessionState(await authApi.setup(input)))
  }, [])

  const logout = useCallback(
    async (message = '') => {
      try {
        await authApi.logout()
      } finally {
        clearSession(message)
      }
    },
    [clearSession],
  )

  const refresh = useCallback(async () => {
    try {
      setState(sessionState(await authApi.me()))
    } catch (cause) {
      if (cause instanceof UnauthorizedError) clearSession(cause.message)
      throw cause
    }
  }, [clearSession])

  const retry = useCallback(() => {
    setState(current => ({ ...current, status: 'loading', error: '' }))
    setAttempt(value => value + 1)
  }, [])

  const can = useCallback(
    (permission: string | string[], mode: 'all' | 'any' = 'all') => hasPermission(state.permissions, permission, mode),
    [state.permissions],
  )

  const value = useMemo<AuthContextValue>(
    () => ({
      status: state.status,
      user: state.user,
      permissions: state.permissions,
      error: state.error,
      can,
      login,
      logout,
      refresh,
      retry,
      completeSetup,
    }),
    [state.status, state.user, state.permissions, state.error, can, login, logout, refresh, retry, completeSetup],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components -- the provider and its consumer hook stay together
export function useAuth() {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth must be used within AuthProvider.')
  return value
}
