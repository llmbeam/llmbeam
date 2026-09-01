import { describe, expect, it } from 'vitest'
import { encodeRequest, FrameReader } from '../../remote-web/src/transport'

describe('remote transport protocol', () => {
  it('encodes request metadata and the virtual cookie jar', async () => {
    const request = new Request('https://example.test/api/chat?model=one', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Origin: 'https://example.test' },
      body: '{"hello":"world"}',
    })
    const encoded = await encodeRequest(request, { sc_session: 'secret' })
    const size = new DataView(encoded.buffer).getUint32(0)
    const payload = JSON.parse(new TextDecoder().decode(encoded.slice(4))) as {
      method: string
      path: string
      headers: Record<string, string[]>
      body: string
    }

    expect(size).toBe(encoded.byteLength - 4)
    expect(payload.method).toBe('POST')
    expect(payload.path).toBe('/api/chat?model=one')
    expect(payload.body).toBe('{"hello":"world"}')
    expect(payload.headers.Cookie).toEqual(['sc_session=secret'])
    expect(payload.headers.origin).toBeUndefined()
  })

  it('reassembles response frames split across TCP reads', async () => {
    const frame = new Uint8Array([0, 0, 0, 6, 2, 104, 101, 108, 108, 111])
    const chunks = [frame.slice(0, 2), frame.slice(2, 7), frame.slice(7), null]
    const reader = new FrameReader({
      read: async () => chunks.shift() ?? null,
      write: async () => {},
      close: () => {},
    })

    const response = await reader.read()
    expect(response.type).toBe(2)
    expect(new TextDecoder().decode(response.payload)).toBe('hello')
  })
})
