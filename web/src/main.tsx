import { render } from 'preact'
import './style.css'

function App() {
  return (
    <main class="boot" aria-live="polite">
      <div class="mark" aria-hidden="true">
        sc
      </div>
      <div>
        <h1>scanchat</h1>
        <p>One binary. Any local LLM. Scan and chat.</p>
      </div>
      <div class="status">
        <span class="status-dot" aria-hidden="true" />
        Web client ready
      </div>
    </main>
  )
}

const root = document.getElementById('app')
if (!root) throw new Error('missing #app root')
render(<App />, root)
