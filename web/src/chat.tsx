import { useEffect, useRef, useState } from 'preact/hooks'
import { type ChatMessage, listModels, type ModelInfo, streamChat } from './api'
import { render } from './markdown'

type Message = ChatMessage & { role: 'user' | 'assistant' | 'system' }

export function Chat() {
  const [models, setModels] = useState<ModelInfo[]>([])
  const [model, setModel] = useState(readStoredModel)
  const [messages, setMessages] = useState<Message[]>([])
  const [draft, setDraft] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const listRef = useRef<HTMLDivElement | null>(null)
  const controllerRef = useRef<AbortController | null>(null)
  const frameRef = useRef<number | null>(null)
  const pendingRef = useRef('')

  async function load() {
    setLoading(true)
    setLoadError('')
    try {
      const available = await listModels()
      setModels(available)
      setModel((selected) => {
        const next = available.some((item) => item.id === selected)
          ? selected
          : available[0]?.id ?? ''
        storeModel(next)
        return next
      })
    } catch (error) {
      if (error instanceof Error && error.message === 'unauthenticated') {
        location.hash = '#/pair'
        location.reload()
        return
      }
      setModels([])
      setLoadError('Could not reach LLMBeam. Check the computer and try again.')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    return () => {
      controllerRef.current?.abort()
      if (frameRef.current !== null) cancelAnimationFrame(frameRef.current)
    }
  }, [])

  useEffect(() => {
    const list = listRef.current
    if (list) list.scrollTop = list.scrollHeight
  }, [messages, streaming])

  function flushDelta() {
    const delta = pendingRef.current
    pendingRef.current = ''
    if (!delta) return
    setMessages((current) => {
      const next = [...current]
      const last = next.at(-1)
      if (last?.role === 'assistant') {
        next[next.length - 1] = { ...last, content: last.content + delta }
      }
      return next
    })
  }

  function queueDelta(delta: string) {
    pendingRef.current += delta
    if (frameRef.current !== null) return
    frameRef.current = requestAnimationFrame(() => {
      frameRef.current = null
      flushDelta()
    })
  }

  function finishDeltas() {
    if (frameRef.current !== null) cancelAnimationFrame(frameRef.current)
    frameRef.current = null
    flushDelta()
  }

  async function send() {
    const content = draft.trim()
    if (!content || !model || streaming || controllerRef.current) return

    const user: Message = { role: 'user', content }
    const history: ChatMessage[] = [...messages, user]
      .filter((message) => message.role !== 'system')
      .map(({ role, content: text }) => ({ role, content: text }))
    setMessages((current) => [...current, user, { role: 'assistant', content: '' }])
    setDraft('')
    setStreaming(true)

    const controller = new AbortController()
    controllerRef.current = controller
    try {
      await streamChat(model, history, queueDelta, controller.signal)
      finishDeltas()
    } catch (error) {
      finishDeltas()
      if (controller.signal.aborted || isAbort(error)) {
        setMessages((current) => removeEmptyAssistant(current))
      } else {
        setMessages((current) => [
          ...removeEmptyAssistant(current),
          { role: 'system', content: errorMessage(error) },
        ])
      }
    } finally {
      controllerRef.current = null
      setStreaming(false)
    }
  }

  if (loading) return <ModelState loading onRetry={load} />
  if (!models.length) return <ModelState error={loadError} onRetry={load} />

  const groups = groupModels(models)
  return (
    <main class="chat-shell">
      <header class="chat-header">
        <div class="brand"><span aria-hidden="true">LB</span>LLMBeam</div>
        <label class="sr-only" for="model-select">Model</label>
        <select
          id="model-select"
          value={model}
          disabled={streaming}
          onChange={(event) => {
            setModel(event.currentTarget.value)
            storeModel(event.currentTarget.value)
          }}
        >
          {[...groups].map(([backend, items]) => (
            <optgroup label={backend} key={backend}>
              {items.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}
            </optgroup>
          ))}
        </select>
      </header>

      <div class="message-list" ref={listRef} role="log" aria-label="Conversation">
        {!messages.length && (
          <div class="empty-chat">
            <p class="eyebrow">Local and private</p>
            <h1>What can I help with?</h1>
            <p>Messages go directly to the model running on your computer.</p>
          </div>
        )}
        {messages.map((message, index) => (
          <article class={`message ${message.role}`} key={index}>
            {message.role === 'system' ? message.content : (
              <div dangerouslySetInnerHTML={{ __html: render(message.content) }} />
            )}
            {streaming && index === messages.length - 1 && message.role === 'assistant' && (
              <span class="stream-cursor" aria-label="Generating" />
            )}
          </article>
        ))}
      </div>

      <form class="composer" onSubmit={(event) => { event.preventDefault(); void send() }}>
        <textarea
          value={draft}
          rows={1}
          placeholder="Message your local model…"
          aria-label="Message"
          disabled={streaming}
          onInput={(event) => setDraft(event.currentTarget.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault()
              void send()
            }
          }}
        />
        {streaming ? (
          <button class="stop" type="button" onClick={() => controllerRef.current?.abort()}>
            Stop
          </button>
        ) : (
          <button type="submit" disabled={!draft.trim() || !model}>Send</button>
        )}
      </form>
    </main>
  )
}

function ModelState({ loading = false, error = '', onRetry }: {
  loading?: boolean
  error?: string
  onRetry: () => Promise<void>
}) {
  return (
    <main class="center model-state" aria-live="polite">
      <div class="mark" aria-hidden="true">LB</div>
      {loading ? <div class="connecting"><span class="spinner" />Finding models…</div> : (
        <>
          <h1>No models found.</h1>
          <p>{error || 'Is Ollama / LM Studio running on your computer?'}</p>
          <button onClick={() => void onRetry()}>Retry</button>
        </>
      )}
    </main>
  )
}

function groupModels(models: ModelInfo[]) {
  const groups = new Map<string, ModelInfo[]>()
  for (const item of models) groups.set(item.backend, [...(groups.get(item.backend) ?? []), item])
  return groups
}

function removeEmptyAssistant(messages: Message[]) {
  return messages.at(-1)?.role === 'assistant' && !messages.at(-1)?.content
    ? messages.slice(0, -1)
    : messages
}

function isAbort(error: unknown) {
  return error instanceof DOMException && error.name === 'AbortError'
}

function errorMessage(error: unknown) {
  return error instanceof Error ? `Generation failed: ${error.message}` : 'Generation failed. Please try again.'
}

function readStoredModel() {
  try { return localStorage.getItem('llmbeam_model') ?? '' } catch { return '' }
}

function storeModel(model: string) {
  try { if (model) localStorage.setItem('llmbeam_model', model) } catch { /* storage may be unavailable */ }
}
