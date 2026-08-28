export interface ModelInfo {
  id: string
  backend: string
  name: string
}

export interface ChatMessage {
  role: string
  content: string
}

export async function pairWithCode(code: string): Promise<boolean> {
  const response = await fetch('/api/pair', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code }),
  })
  return response.ok
}

export async function hasSession(): Promise<boolean> {
  const response = await fetch('/api/session')
  return response.ok
}

export async function listModels(): Promise<ModelInfo[]> {
  const response = await fetch('/api/models')
  if (response.status === 401) throw new Error('unauthenticated')
  if (!response.ok) throw new Error(`models failed: ${response.status}`)

  const payload = (await response.json()) as { models?: ModelInfo[] }
  return payload.models ?? []
}

export async function streamChat(
  model: string,
  messages: ChatMessage[],
  onDelta: (text: string) => void,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch('/api/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, messages }),
    signal,
  })
  if (!response.ok || !response.body) {
    throw new Error(`chat failed: ${response.status}`)
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { done, value } = await reader.read()
    buffer += decoder.decode(value, { stream: !done })

    const events = buffer.replaceAll('\r\n', '\n').split('\n\n')
    buffer = events.pop() ?? ''
    for (const event of events) {
      if (consumeEvent(event, onDelta)) return
    }
    if (done) break
  }

  if (buffer && consumeEvent(buffer, onDelta)) return
  throw new Error('chat stream ended unexpectedly')
}

function consumeEvent(event: string, onDelta: (text: string) => void): boolean {
  const payload = event
    .split('\n')
    .filter((line) => line.startsWith('data:'))
    .map((line) => line.slice(5).trimStart())
    .join('\n')
    .trim()

  if (!payload) return false
  if (payload === '[DONE]') return true

  try {
    const parsed = JSON.parse(payload) as {
      choices?: { delta?: { content?: string } }[]
    }
    const delta = parsed.choices?.[0]?.delta?.content
    if (delta) onDelta(delta)
  } catch {
    // Ignore malformed upstream events; later valid events remain readable.
  }
  return false
}
