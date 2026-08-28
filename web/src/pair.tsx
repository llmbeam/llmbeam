import { useEffect, useState } from 'preact/hooks'
import { pairWithCode } from './api'

const pairError =
  'Invalid or expired code — check the terminal on your computer.'

interface PairProps {
  code?: string
  onPaired: () => void
}

export function Pair({ code, onPaired }: PairProps) {
  const [value, setValue] = useState(() => formatCode(code ?? ''))
  const [connecting, setConnecting] = useState(Boolean(code))
  const [error, setError] = useState('')

  useEffect(() => {
    if (!code) return
    setValue(formatCode(code))
    void connect(code)
  }, [code])

  async function connect(candidate: string) {
    setConnecting(true)
    setError('')
    try {
      if (await pairWithCode(normalizeCode(candidate))) {
        onPaired()
        return
      }
    } catch {
      // A network failure uses the same actionable recovery as an invalid code.
    }
    setConnecting(false)
    setError(pairError)
  }

  if (connecting) {
    return (
      <main class="center" aria-live="polite">
        <div class="mark" aria-hidden="true">sc</div>
        <div class="connecting">
          <span class="spinner" aria-hidden="true" />
          Connecting…
        </div>
      </main>
    )
  }

  return (
    <main class="center">
      <section class="pair-card" aria-labelledby="pair-title">
        <div class="mark" aria-hidden="true">sc</div>
        <div>
          <p class="eyebrow">Secure local access</p>
          <h1 id="pair-title">Connect to scanchat</h1>
          <p>Enter the pairing code shown in your computer terminal.</p>
        </div>
        <form onSubmit={(event) => { event.preventDefault(); void connect(value) }}>
          <label for="pair-code">Pairing code</label>
          <input
            id="pair-code"
            value={value}
            onInput={(event) => setValue(formatCode(event.currentTarget.value))}
            placeholder="ABCD-EFGH"
            inputMode="text"
            autoComplete="one-time-code"
            autoCapitalize="characters"
            spellcheck={false}
            maxlength={9}
            autofocus
            aria-describedby={error ? 'pair-error' : undefined}
            aria-invalid={Boolean(error)}
          />
          <button type="submit" disabled={normalizeCode(value).length !== 8}>
            Connect
          </button>
          <p id="pair-error" class="pair-error" role="alert">{error}</p>
        </form>
      </section>
    </main>
  )
}

function normalizeCode(code: string): string {
  return code.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 8)
}

function formatCode(code: string): string {
  const normalized = normalizeCode(code)
  return normalized.length > 4
    ? `${normalized.slice(0, 4)}-${normalized.slice(4)}`
    : normalized
}
