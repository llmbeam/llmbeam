const escapeHTML = (value: string) =>
  value.replace(/[&<>"']/g, (character) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  })[character] ?? character)

function inline(value: string): string {
  const parts = value.split(/(`[^`\n]+`)/g)
  return parts.map((part) => {
    if (part.startsWith('`') && part.endsWith('`')) {
      return `<code>${part.slice(1, -1)}</code>`
    }
    return part
      .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
      .replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>')
  }).join('')
}

export function render(markdown: string): string {
  const lines = escapeHTML(markdown).replaceAll('\r\n', '\n').split('\n')
  const output: string[] = []
  let paragraph: string[] = []
  let list: string[] = []
  let code: string[] | undefined
  let language = ''

  const flushParagraph = () => {
    if (paragraph.length) output.push(`<p>${inline(paragraph.join('<br>'))}</p>`)
    paragraph = []
  }
  const flushList = () => {
    if (list.length) output.push(`<ul>${list.map((item) => `<li>${inline(item)}</li>`).join('')}</ul>`)
    list = []
  }
  const flushCode = () => {
    if (!code) return
    const className = language ? ` class="language-${language}"` : ''
    output.push(`<pre><code${className}>${code.join('\n')}</code></pre>`)
    code = undefined
    language = ''
  }

  for (const line of lines) {
    if (code) {
      if (line.startsWith('```')) flushCode()
      else code.push(line)
      continue
    }
    if (line.startsWith('```')) {
      flushParagraph(); flushList()
      language = line.slice(3).trim().replace(/[^a-zA-Z0-9_+-]/g, '')
      code = []
      continue
    }
    const heading = /^(#{1,3})\s+(.+)$/.exec(line)
    if (heading) {
      flushParagraph(); flushList()
      output.push(`<h${heading[1].length}>${inline(heading[2])}</h${heading[1].length}>`)
      continue
    }
    const item = /^[-*]\s+(.+)$/.exec(line)
    if (item) {
      flushParagraph()
      list.push(item[1])
      continue
    }
    if (!line.trim()) {
      flushParagraph(); flushList()
      continue
    }
    flushList()
    paragraph.push(line)
  }
  flushParagraph(); flushList(); flushCode()
  return output.join('\n')
}
