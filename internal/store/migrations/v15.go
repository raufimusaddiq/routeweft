package migrations

// v15 gives a proxy pool the fields the reference serves beside its URL (#272):
// the kind of endpoint it is (a forward proxy, or a Vercel, Cloudflare, Deno or
// Netlify relay that carries the request in x-relay-* headers), whether it is
// strict (a connection bound to it never leaves over the direct address, even
// when the pool is unusable), the hosts that bypass it, and the outcome of the last
// connectivity test. Existing pools become plain forward proxies that are not
// strict, with no bypass list and an unknown test status, which is what they
// already behaved as. None of the new columns holds a credential. Forward-only: an
// older binary ignores the added columns, but rollback still requires the
// pre-upgrade backup.
var v15 = Migration{
	Version: 15,
	Name:    "proxy_pool_kind_strict_status",
	SQL: `
ALTER TABLE proxy_pools ADD COLUMN kind TEXT NOT NULL DEFAULT 'http' CHECK (kind IN ('http','vercel','cloudflare','deno','netlify'));
ALTER TABLE proxy_pools ADD COLUMN strict INTEGER NOT NULL DEFAULT 0 CHECK (strict IN (0,1));
ALTER TABLE proxy_pools ADD COLUMN no_proxy TEXT NOT NULL DEFAULT '';
ALTER TABLE proxy_pools ADD COLUMN test_status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE proxy_pools ADD COLUMN last_tested_at TEXT;
ALTER TABLE proxy_pools ADD COLUMN last_error TEXT;
`,
}
