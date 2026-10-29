// Resolves the parity oracle for one run (#289).
//
//   node compat/parity/resolve-oracle.mjs                    # LiteRouter refs/heads/main, resolved now
//   node compat/parity/resolve-oracle.mjs --sha <40-hex>     # reproduce a recorded run
//
// refs/heads/main on raufimusaddiq/literouter is resolved exactly once here, that
// commit is fetched into ORACLE_DIR, and the run uses only that SHA (ORACLE_SHA)
// from then on; nothing re-resolves the branch. A failed resolve or fetch is an
// error: there is no cached or last-known SHA to fall back to. The commit is
// recorded in oracle-run.json (and the CI step summary) so the run can be repeated
// with --sha. The remote is read only; this never writes to it.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { appendFileSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const defaultRemote = 'https://github.com/raufimusaddiq/literouter.git';
export const oracleRef = 'refs/heads/main';
const sha40 = /^[0-9a-f]{40}$/;

const git = (args, options = {}) => execFileSync('git', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], ...options });

function failure(step, error) {
  const detail = String(error?.stderr || error?.message || error).trim();
  return new Error(`oracle: ${step} failed; refusing to continue without a freshly resolved commit: ${detail}`);
}

// resolveOracle resolves (or, with sha, reproduces) the oracle commit and fetches
// it into dir. It returns the run record; it throws, and writes no record, if any
// step fails.
export function resolveOracle({ remote = defaultRemote, sha, dir, now = () => new Date() } = {}) {
  let commit = sha;
  if (commit !== undefined) {
    assert.match(commit, sha40, 'oracle: --sha must be a full 40-hex commit');
  } else {
    let listing;
    try {
      listing = git(['ls-remote', remote, oracleRef]);
    } catch (error) {
      throw failure(`resolving ${oracleRef} on ${remote}`, error);
    }
    const lines = listing.split('\n').filter(Boolean);
    const [id, name] = (lines[0] ?? '').split('\t');
    if (lines.length !== 1 || name !== oracleRef || !sha40.test(id)) {
      throw failure(`resolving ${oracleRef} on ${remote}`, `expected exactly one ${oracleRef} line, got ${JSON.stringify(listing.trim())}`);
    }
    commit = id;
  }
  dir ??= mkdtempSync(join(tmpdir(), 'routeweft-oracle-'));
  try {
    mkdirSync(dir, { recursive: true });
    git(['init', '--bare', '--quiet', dir]);
    git(['-C', dir, 'fetch', '--quiet', '--depth=1', remote, commit]);
    assert.equal(git(['-C', dir, 'rev-parse', `${commit}^{commit}`]).trim(), commit);
  } catch (error) {
    throw failure(`fetching ${commit} from ${remote}`, error);
  }
  return {
    oracle: 'raufimusaddiq/literouter',
    remote,
    ref: oracleRef,
    sha: commit,
    mode: sha === undefined ? 'resolved-main' : 'recorded-sha',
    resolvedAt: now().toISOString(),
    dir,
  };
}

function main(argv, env) {
  const flag = argv.indexOf('--sha');
  const sha = flag >= 0 ? argv[flag + 1] : env.ORACLE_REPRODUCE_SHA || undefined;
  const record = resolveOracle({ remote: env.ORACLE_REMOTE || defaultRemote, sha, dir: env.ORACLE_DIR || undefined });
  const recordPath = env.ORACLE_RECORD || join(record.dir, 'oracle-run.json');
  writeFileSync(recordPath, `${JSON.stringify(record, null, 2)}\n`);
  const lines = [`ORACLE_DIR=${record.dir}`, `ORACLE_SHA=${record.sha}`, `ORACLE_RECORD=${recordPath}`];
  console.log(lines.join('\n'));
  if (env.GITHUB_ENV) appendFileSync(env.GITHUB_ENV, `${lines.join('\n')}\n`);
  if (env.GITHUB_STEP_SUMMARY) {
    appendFileSync(env.GITHUB_STEP_SUMMARY, `Parity oracle: \`${record.oracle}\` at \`${record.sha}\` (${record.mode}, ${record.resolvedAt}). Reproduce with \`node compat/parity/resolve-oracle.mjs --sha ${record.sha}\`.\n`);
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    main(process.argv.slice(2), process.env);
  } catch (error) {
    console.error(error.message);
    process.exit(1);
  }
}
