package migrations

// v8 moves the MiMo free path out of the stored base URL into the provider's
// transport endpoint (internal/providers/registry), so a built-in node that still
// holds the old default would otherwise send .../chat/chat/completions. Only rows
// whose base_url is exactly the old default are rewritten; an operator override
// is left alone. Forward-only; rollback requires the pre-upgrade backup.
var v8 = Migration{
	Version: 8,
	Name:    "mimo_free_base_url",
	SQL: `UPDATE provider_nodes SET base_url='https://api.xiaomimimo.com'
WHERE kind='builtin' AND provider_id IN ('mimo-free','mmf')
  AND base_url='https://api.xiaomimimo.com/api/free-ai/openai/chat';`,
}
