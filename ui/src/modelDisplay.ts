// The model row's secondary line. A discovered model can carry no capabilities:
// the admin API sends `capabilities: null` for those rows, so the list is
// optional here and an empty one reads as an em dash rather than blank text.
export type ModelFacts = { contextWindow?: number; capabilities?: string[] | null }

export function modelFacts(model: ModelFacts): string {
  if (model.contextWindow) return `${model.contextWindow.toLocaleString()} tokens`
  return (model.capabilities ?? []).join(', ') || '—'
}
