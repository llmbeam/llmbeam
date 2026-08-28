import { describe, expect, it } from 'vitest'
import { render } from './markdown'

describe('render', () => {
  it('escapes HTML before rendering markdown', () => {
    expect(render('<script>alert("x")</script>')).toContain(
      '&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;',
    )
  })

  it('renders fenced code blocks', () => {
    expect(render('```ts\nconst x = 1\n```')).toBe(
      '<pre><code class="language-ts">const x = 1</code></pre>',
    )
  })

  it('renders bold text', () => {
    expect(render('hello **world**')).toBe('<p>hello <strong>world</strong></p>')
  })

  it('renders unordered lists', () => {
    expect(render('- one\n- two')).toBe('<ul><li>one</li><li>two</li></ul>')
  })

  it('renders headings', () => {
    expect(render('## Local model')).toBe('<h2>Local model</h2>')
  })
})
