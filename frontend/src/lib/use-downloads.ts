import { useCallback, useEffect, useRef, useState } from 'react'
import { api, errorMessage, isActiveJob, type Job, type Release } from '@/lib/api'

const activeIntervalMs = 1500
const idleIntervalMs = 5000

export function useDownloads(enabled = true) {
  const [jobs, setJobs] = useState<Job[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [version, setVersion] = useState(0)
  const jobsRef = useRef<Job[] | null>(null)

  useEffect(() => {
    if (!enabled) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    let stopped = false

    const applyJobs = (next: Job[]) => {
      const sorted = [...next].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
      jobsRef.current = sorted
      setJobs(sorted)
    }

    const poll = async () => {
      try {
        const next = await api.listDownloads(controller.signal)
        if (stopped) return
        applyJobs(next)
        setError(null)
      } catch (cause) {
        if (stopped || controller.signal.aborted) return
        setError(errorMessage(cause))
      }
      if (stopped) return
      const active = (jobsRef.current ?? []).some(isActiveJob)
      timer = setTimeout(poll, active ? activeIntervalMs : idleIntervalMs)
    }

    void poll()
    return () => {
      stopped = true
      controller.abort()
      clearTimeout(timer)
    }
  }, [version, enabled])

  const refresh = useCallback(() => setVersion((current) => current + 1), [])

  const replaceJob = useCallback((job: Job) => {
    const next = [job, ...(jobsRef.current ?? []).filter((current) => current.id !== job.id)]
    jobsRef.current = next
    setJobs(next)
  }, [])

  const download = useCallback(
    async (release: Release) => {
      const job = await api.createDownload(release)
      replaceJob(job)
      refresh()
    },
    [refresh, replaceJob],
  )

  const retry = useCallback(
    async (job: Job) => {
      replaceJob(await api.retryDownload(job.id))
      refresh()
    },
    [refresh, replaceJob],
  )

  const act = useCallback(
    async (action: (id: string) => Promise<Job>, job: Job) => {
      replaceJob(await action(job.id))
      refresh()
    },
    [refresh, replaceJob],
  )

  const pause = useCallback((job: Job) => act(api.pauseDownload, job), [act])
  const resume = useCallback((job: Job) => act(api.resumeDownload, job), [act])
  const cancel = useCallback((job: Job) => act(api.cancelDownload, job), [act])

  return { jobs, error, refresh, download, retry, pause, resume, cancel }
}
