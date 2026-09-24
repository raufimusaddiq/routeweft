import React from 'react'
import { createRoot } from 'react-dom/client'
import './style.css'

function App() {
  return (
    <main>
      <div className="brand-mark" aria-hidden="true">R</div>
      <p className="eyebrow">ROUTEWEFT / CONTROL PLANE</p>
      <h1>Gateway ready for configuration.</h1>
      <p className="description">The Routeweft service shell is running. Provider and routing workflows arrive in later implementation increments.</p>
      <div className="status"><span className="indicator" /> Service shell <code>v0.1.0</code></div>
    </main>
  )
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode><App /></React.StrictMode>,
)
