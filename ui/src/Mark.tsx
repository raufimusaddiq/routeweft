import markSvg from '../public/mark.svg?raw'

// The Routeweft mark. public/mark.svg is the single drawing; it is inlined here so it inherits colour from the UI:
// the threads use currentColor and one thread uses var(--mark-accent, currentColor), so light and dark need no second asset.
export function Mark() {
  return <span className="brand-mark" aria-hidden="true" dangerouslySetInnerHTML={{ __html: markSvg }} />
}
