import '../../web/src/style.css'
import { installRemoteTransport } from './transport'

async function start() {
  try {
    window.__LLMBEAM_FETCH__ = await installRemoteTransport()
    await import('../../web/src/main')
  } catch (error) {
    showFailure(error)
  }
}

function showFailure(error: unknown) {
  const detail = error instanceof Error ? error.message : String(error)
  const root = document.getElementById('app')
  if (!root) return
  root.innerHTML = `
    <main class="center model-state" aria-live="assertive">
      <div class="mark" aria-hidden="true">LB</div>
      <h1>Connection failed.</h1>
      <p>${escapeHTML(detail)}</p>
      <button type="button" id="retry-connection">Retry</button>
    </main>`
  document.getElementById('retry-connection')?.addEventListener('click', () => location.reload())
}

function escapeHTML(value: string) {
  const element = document.createElement('div')
  element.textContent = value
  return element.innerHTML
}

void start()
