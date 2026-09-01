const DERP_MAP_URL = 'https://tailcat.dev/derpmap.json'
const TUNNEL_PORT = 443
const MAX_FRAME_SIZE = 12 << 20
const ADDRESS_KEY = 'llmbeam_remote_address'
const COOKIES_KEY = 'llmbeam_remote_cookies'

const FRAME_HEADERS = 1
const FRAME_BODY = 2
const FRAME_END = 3

interface GoRuntime {
  importObject: WebAssembly.Imports
  run(instance: WebAssembly.Instance): Promise<void>
}

interface TailcatConnection {
  read(): Promise<Uint8Array | null>
  write(data: Uint8Array): Promise<void>
  close(): void
}

interface TailcatDialOptions {
  addr: string
  derpMapURL: string
  port: number
  verbose: boolean
}

interface ResponseHead {
  status: number
  headers: Record<string, string[]>
}

interface ResponseFrame {
  type: number
  payload: Uint8Array
}

declare global {
  var Go: { new(): GoRuntime }
  var onTailcatReady: (() => void) | undefined
  function tailcatDial(options: TailcatDialOptions): Promise<TailcatConnection>
}

export async function installRemoteTransport(): Promise<typeof fetch> {
  const address = await resolveAddress()
  await loadTailcat()
  const transport = new RemoteTransport(address)
  return transport.fetch.bind(transport) as typeof fetch
}

async function resolveAddress(): Promise<string> {
  const match = location.hash.match(/^#\/connect\/([^/?#]+)\/([^/?#]+)\/?$/)
  if (match) {
    const address = await decodeAddress(decodeFragmentPart(match[1]))
    const code = decodeFragmentPart(match[2])
    const previous = readSession(ADDRESS_KEY)
    if (previous !== address) removeSession(COOKIES_KEY)
    writeSession(ADDRESS_KEY, address)
    history.replaceState(null, '', `${location.pathname}${location.search}#/pair/${encodeURIComponent(code)}`)
    return address
  }

  const stored = readSession(ADDRESS_KEY)
  if (!stored) throw new Error('Scan the current remote QR code from the LLMBeam terminal.')
  return stored
}

function decodeFragmentPart(value: string) {
  try { return decodeURIComponent(value) } catch { return value }
}

async function decodeAddress(value: string): Promise<string> {
  if (!value.startsWith('tg')) return value
  if (typeof globalThis.DecompressionStream !== 'function') {
    throw new Error('This browser is too old for the secure remote connection.')
  }
  const compressed = decodeBase64URL(value.slice(2))
  const stream = new Blob([compressed.buffer as ArrayBuffer]).stream()
    .pipeThrough(new DecompressionStream('gzip'))
  const raw = new Uint8Array(await new Response(stream).arrayBuffer())
  return `tc${encodeBase64URL(raw)}`
}

function decodeBase64URL(value: string): Uint8Array {
  const padded = value.replaceAll('-', '+').replaceAll('_', '/')
    .padEnd(Math.ceil(value.length / 4) * 4, '=')
  const binary = atob(padded)
  return Uint8Array.from(binary, (character) => character.charCodeAt(0))
}

function encodeBase64URL(value: Uint8Array): string {
  let binary = ''
  for (const byte of value) binary += String.fromCharCode(byte)
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '')
}

async function loadTailcat() {
  if (typeof globalThis.Go !== 'function') {
    throw new Error('Tailcat runtime did not load.')
  }
  if (typeof globalThis.DecompressionStream !== 'function') {
    throw new Error('This browser is too old for the secure remote connection.')
  }

  const ready = new Promise<void>((resolve) => { globalThis.onTailcatReady = resolve })
  const response = await fetch('./main.wasm.gz')
  if (!response.ok || !response.body) {
    throw new Error(`Could not download secure connection module (${response.status}).`)
  }
  const decompressed = response.body.pipeThrough(new DecompressionStream('gzip'))
  const go = new globalThis.Go()
  const module = await WebAssembly.instantiateStreaming(
    new Response(decompressed, { headers: { 'Content-Type': 'application/wasm' } }),
    go.importObject,
  )
  void go.run(module.instance)
  await ready
  globalThis.onTailcatReady = undefined
  if (typeof globalThis.tailcatDial !== 'function') {
    throw new Error('Tailcat connection module did not initialize.')
  }
}

class RemoteTransport {
  private connection: TailcatConnection | undefined
  private turn: Promise<void> = Promise.resolve()
  private readonly cookies = loadCookies()
  private readonly address: string

  constructor(address: string) {
    this.address = address
  }

  async fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const release = await this.acquire()
    let releaseOnExit = true
    try {
      const request = new Request(input, init)
      if (request.signal.aborted) throw abortError()
      const connection = await this.connect()
      const reader = new FrameReader(connection)
      await connection.write(await encodeRequest(request, this.cookies))
      const head = decodeHead(await reader.read())
      this.captureCookies(head.headers)

      if (new URL(request.url, location.href).pathname !== '/api/chat') {
        const body = await readResponseBody(reader)
        return new Response(body, responseInit(head))
      }

      releaseOnExit = false
      return new Response(this.streamingBody(reader, request.signal, release), responseInit(head))
    } catch (error) {
      this.disconnect()
      throw error
    } finally {
      if (releaseOnExit) release()
    }
  }

  private async acquire(): Promise<() => void> {
    const previous = this.turn
    let release = () => {}
    this.turn = new Promise<void>((resolve) => { release = resolve })
    await previous
    return release
  }

  private async connect() {
    if (!this.connection) {
      this.connection = await globalThis.tailcatDial({
        addr: this.address,
        derpMapURL: DERP_MAP_URL,
        port: TUNNEL_PORT,
        verbose: false,
      })
    }
    return this.connection
  }

  private disconnect() {
    this.connection?.close()
    this.connection = undefined
  }

  private streamingBody(reader: FrameReader, signal: AbortSignal, release: () => void) {
    let finished = false
    const finish = (disconnect: boolean) => {
      if (finished) return
      finished = true
      signal.removeEventListener('abort', onAbort)
      if (disconnect) this.disconnect()
      release()
    }
    const onAbort = () => finish(true)
    signal.addEventListener('abort', onAbort, { once: true })

    return new ReadableStream<Uint8Array>({
      pull: async (controller) => {
        try {
          if (signal.aborted) throw abortError()
          const frame = await reader.read()
          if (frame.type === FRAME_BODY) {
            controller.enqueue(frame.payload)
            return
          }
          if (frame.type !== FRAME_END) throw new Error(`Unexpected response frame ${frame.type}.`)
          finish(false)
          controller.close()
        } catch (error) {
          finish(true)
          controller.error(error)
        }
      },
      cancel: () => finish(true),
    })
  }

  private captureCookies(headers: Record<string, string[]>) {
    for (const [name, values] of Object.entries(headers)) {
      if (name.toLowerCase() !== 'set-cookie') continue
      for (const value of values) {
        const pair = value.split(';', 1)[0]
        const separator = pair.indexOf('=')
        if (separator <= 0) continue
        const cookieName = pair.slice(0, separator).trim()
        const cookieValue = pair.slice(separator + 1).trim()
        if (cookieValue) this.cookies[cookieName] = cookieValue
        else delete this.cookies[cookieName]
      }
    }
    writeSession(COOKIES_KEY, JSON.stringify(this.cookies))
  }
}

export class FrameReader {
  private buffered = new Uint8Array()
  private readonly connection: TailcatConnection

  constructor(connection: TailcatConnection) {
    this.connection = connection
  }

  async read(): Promise<ResponseFrame> {
    const header = await this.readBytes(5)
    const view = new DataView(header.buffer, header.byteOffset, header.byteLength)
    const size = view.getUint32(0)
    if (size < 1 || size > MAX_FRAME_SIZE) throw new Error(`Invalid response frame size ${size}.`)
    return { type: header[4], payload: await this.readBytes(size - 1) }
  }

  private async readBytes(size: number): Promise<Uint8Array> {
    while (this.buffered.byteLength < size) {
      const chunk = await this.connection.read()
      if (!chunk) throw new Error('Remote LLMBeam closed the connection.')
      const combined = new Uint8Array(this.buffered.byteLength + chunk.byteLength)
      combined.set(this.buffered)
      combined.set(chunk, this.buffered.byteLength)
      this.buffered = combined
    }
    const result = this.buffered.slice(0, size)
    this.buffered = this.buffered.slice(size)
    return result
  }
}

export async function encodeRequest(request: Request, cookies: Record<string, string>) {
  const headers: Record<string, string[]> = {}
  request.headers.forEach((value, name) => {
    if (name.toLowerCase() !== 'origin' && name.toLowerCase() !== 'cookie') headers[name] = [value]
  })
  const cookie = Object.entries(cookies).map(([name, value]) => `${name}=${value}`).join('; ')
  if (cookie) headers.Cookie = [cookie]
  if (!request.headers.has('user-agent') && typeof navigator !== 'undefined') {
    headers['User-Agent'] = [navigator.userAgent]
  }

  const url = new URL(request.url)
  const payload = new TextEncoder().encode(JSON.stringify({
    method: request.method,
    path: `${url.pathname}${url.search}`,
    headers,
    body: request.method === 'GET' || request.method === 'HEAD' ? '' : await request.text(),
  }))
  if (!payload.byteLength || payload.byteLength > MAX_FRAME_SIZE) {
    throw new Error('Request is too large for the remote connection.')
  }
  const frame = new Uint8Array(payload.byteLength + 4)
  new DataView(frame.buffer).setUint32(0, payload.byteLength)
  frame.set(payload, 4)
  return frame
}

function decodeHead(frame: ResponseFrame): ResponseHead {
  if (frame.type !== FRAME_HEADERS) throw new Error(`Unexpected response frame ${frame.type}.`)
  const head = JSON.parse(new TextDecoder().decode(frame.payload)) as ResponseHead
  if (!Number.isInteger(head.status) || head.status < 100 || head.status > 599) {
    throw new Error('Remote LLMBeam returned an invalid status.')
  }
  return head
}

async function readResponseBody(reader: FrameReader) {
  const chunks: Uint8Array[] = []
  let size = 0
  for (;;) {
    const frame = await reader.read()
    if (frame.type === FRAME_END) break
    if (frame.type !== FRAME_BODY) throw new Error(`Unexpected response frame ${frame.type}.`)
    chunks.push(frame.payload)
    size += frame.payload.byteLength
  }
  const body = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) {
    body.set(chunk, offset)
    offset += chunk.byteLength
  }
  return body
}

function responseInit(head: ResponseHead): ResponseInit {
  const headers = new Headers()
  for (const [name, values] of Object.entries(head.headers)) {
    if (name.toLowerCase() === 'set-cookie') continue
    for (const value of values) headers.append(name, value)
  }
  return { status: head.status, headers }
}

function loadCookies(): Record<string, string> {
  try {
    const value = readSession(COOKIES_KEY)
    return value ? JSON.parse(value) as Record<string, string> : {}
  } catch {
    return {}
  }
}

function readSession(key: string) {
  try { return sessionStorage.getItem(key) ?? '' } catch { return '' }
}

function writeSession(key: string, value: string) {
  try { sessionStorage.setItem(key, value) } catch { /* private browsing may disable storage */ }
}

function removeSession(key: string) {
  try { sessionStorage.removeItem(key) } catch { /* private browsing may disable storage */ }
}

function abortError() {
  return new DOMException('The operation was aborted.', 'AbortError')
}
