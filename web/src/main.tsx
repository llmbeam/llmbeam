import { render } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { hasSession } from './api'
import { Pair } from './pair'
import './style.css'

type Route = { page: 'checking' } | { page: 'pair'; code?: string } | { page: 'chat' }

function routeFromHash(): Route | undefined {
  const match = location.hash.match(/^#\/pair\/([^/?#]+)\/?$/)
  if (!match) return undefined
  try {
    return { page: 'pair', code: decodeURIComponent(match[1]) }
  } catch {
    return { page: 'pair', code: match[1] }
  }
}

function App() {
  const [route, setRoute] = useState<Route>(() => routeFromHash() ?? { page: 'checking' })

  useEffect(() => {
    let active = true
    let request = 0

    async function resolveRoute() {
      const currentRequest = ++request
      const pairedRoute = routeFromHash()
      if (pairedRoute) {
        setRoute(pairedRoute)
        return
      }
      setRoute({ page: 'checking' })
      try {
        const paired = await hasSession()
        if (active && currentRequest === request) {
          setRoute({ page: paired ? 'chat' : 'pair' })
        }
      } catch {
        if (active && currentRequest === request) setRoute({ page: 'pair' })
      }
    }

    function onHashChange() { void resolveRoute() }

    void resolveRoute()
    addEventListener('hashchange', onHashChange)
    return () => {
      active = false
      removeEventListener('hashchange', onHashChange)
    }
  }, [])

  function paired() {
    history.replaceState(null, '', `${location.pathname}${location.search}#/`)
    setRoute({ page: 'chat' })
  }

  if (route.page === 'pair') return <Pair code={route.code} onPaired={paired} />
  if (route.page === 'chat') return <ChatPlaceholder />
  return <CheckingSession />
}

function CheckingSession() {
  return (
    <main class="center" aria-live="polite">
      <div class="mark" aria-hidden="true">sc</div>
      <div class="connecting">
        <span class="spinner" aria-hidden="true" />
        Checking session…
      </div>
    </main>
  )
}

function ChatPlaceholder() {
  return (
    <main class="center">
      <section class="chat-ready">
        <div class="mark" aria-hidden="true">sc</div>
        <p class="eyebrow">Connected</p>
        <h1>Ready to chat</h1>
        <p>The streaming chat interface arrives in the next implementation step.</p>
      </section>
    </main>
  )
}

const root = document.getElementById('app')
if (!root) throw new Error('missing #app root')
render(<App />, root)
