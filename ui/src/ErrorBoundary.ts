import { Component, Fragment, createElement, createRef, type ErrorInfo, type ReactNode } from 'react'

// The page a hash route names, for the root error message.
export function pageNameFromHash(hash: string): string {
  const slug = hash.replace(/^#/, '').trim()
  if (!slug) return 'Overview'
  const words = slug.split(/[^a-z0-9]+/i).filter(Boolean)
  if (words.length === 0) return 'Overview'
  return words.map(word => word[0].toUpperCase() + word.slice(1)).join(' ')
}

type Props = { children?: ReactNode }
type State = { failed: boolean; page: string; attempt: number }

// One root boundary: a render throw anywhere below it replaces the page with a
// readable message and a Retry that remounts the tree. It logs the error for
// diagnosis but never renders a stack. Fetch failures are not render throws, so
// an auth 401 still reaches onUnauthorized and the sign-in redirect, as before.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { failed: false, page: 'Overview', attempt: 0 }
  private retryButton = createRef<HTMLButtonElement>()

  static getDerivedStateFromError(): Partial<State> {
    // The hash is the page route; name it so the first fallback paint says where
    // the operator was.
    return { failed: true, page: pageNameFromHash(globalThis.location?.hash ?? '') }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Logged for diagnosis; the page never shows a stack.
    console.error(`Routeweft render error: ${error.message}`, error, info.componentStack)
  }

  componentDidUpdate(_previous: Props, previousState: State) {
    if (!previousState.failed && this.state.failed) this.retryButton.current?.focus()
  }

  private retry = () => {
    this.setState(state => ({ failed: false, attempt: state.attempt + 1 }))
  }

  render() {
    if (!this.state.failed) {
      // The key remounts the whole tree, so state left behind by the failed
      // render starts clean.
      return createElement(Fragment, { key: this.state.attempt }, this.props.children)
    }
    return createElement('section', {
      className: 'page-error render-error',
      role: 'alert',
      'aria-labelledby': 'render-error-title',
    },
    createElement('div', null,
      createElement('h2', { id: 'render-error-title' }, `The ${this.state.page} page stopped rendering`),
      createElement('p', null, 'Your configuration and stored credentials are unchanged. Retry to render the page again; if it keeps failing, reload Routeweft.'),
    ),
    createElement('button', { ref: this.retryButton, className: 'button-secondary', type: 'button', onClick: this.retry }, 'Retry'))
  }
}
