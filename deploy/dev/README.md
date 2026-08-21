# Dev Box Deployment (64.227.170.31)

The DigitalOcean dev server runs the **whole stack** — voice worker, manager API,
MQTT gateways and the dashboards — on one host under pm2. It serves
`otadev.cheekoai.in`.

This is the only environment an agent may deploy to without asking. Production
(`ota.cheekoai.in` → 139.59.7.72, see [prod/](../prod/))
and EKS (see [../k8s/](../k8s/) and [../README.md](../README.md)) each need an explicit
per-deploy grant.

## Current Target

| | |
|---|---|
| Host | `root@64.227.170.31` |
| Serves | `otadev.cheekoai.in` |
| Process manager | pm2 (`pm2 list`, `pm2 logs <name>`) |
| Database | Supabase project `tsiocygczplmnjpqmutc` port 5432 — "DB1" |
| Go toolchain | `/usr/local/go/bin` (not on the default PATH) |

**DB1 is not the database your laptop uses.** Local development points at a
different Supabase project (`shlrfpbqkfnxqcmuatvs`, port 6543), so schema and
seed changes must be applied to both or they silently diverge. DB1 has carried
drift that `prisma migrate status` reported as clean — see Common Problems.

## What Runs Here

Two repos, one host:

| pm2 name | repo | path |
|---|---|---|
| `picoclaw-livekit` | picoclaw | `/root/picoclaw` |
| `manager-api` | cheeko-backend | `/root/xiaozhi-esp32-server/main/manager-api-node` |
| `gw-0` … `gw-3` | cheeko-backend | `/root/xiaozhi-esp32-server/main/mqtt-gateway` |
| `manager-web`, `admin-dashboard` | cheeko-backend | same repo |

Config and secrets:

- Worker runtime settings: `/root/.picoclaw/config.json`
- Worker environment: `/root/picoclaw/.env`
- Backend environment: `/root/xiaozhi-esp32-server/main/manager-api-node/.env`

**STT/TTS/LLM provider selection comes from the manager API and overrides the
worker's own defaults.** A global fallback voice shows as `voice_id=pooja`;
per-character voice arrives as a per-session override in room metadata. Do not
chase a wrong voice in `config.json` — check `ai_agent_template` first.

## Shipping Code

There is no CI to this box. Confirm local `HEAD` matches the server's before
you start:

```bash
git -C /root/picoclaw log --oneline -1     # on the box
git log --oneline -1                       # locally
```

Ship only the files you changed, so the rest of the server tree stays clean and
`git checkout -- <file>` remains a working undo:

```bash
tar -cf - pkg/livekit/quiz_state.go | ssh root@64.227.170.31 "tar -xf - -C /root/picoclaw"
```

## Deploy: picoclaw-livekit (Go)

Cross-compiling from Windows does not work — opus needs cgo. Build on the box:

```bash
export PATH=$PATH:/usr/local/go/bin
export CGO_LDFLAGS='-lc++ -lc++abi'
make build-livekit
pm2 restart picoclaw-livekit
```

Verify: `pm2 describe picoclaw-livekit` shows `online`, and
`pm2 logs picoclaw-livekit --lines 30 --nostream` has no `panic`/`fatal`.

## Deploy: manager-api (Node)

```bash
node --check src/<changed file>.js          # syntax before restart
npx prisma generate                         # ONLY if prisma/schema.prisma changed
pm2 restart manager-api --update-env
```

**`npx prisma generate` is mandatory after a schema change.** Skip it and any
`select: { new_field }` throws at runtime after the restart.

**A restart applies every unapplied migration in the tree, in one go.**
`server.js` calls `runPrismaMigrations()` on boot and DB1 has
`_prisma_migrations`. Verified 2026-08-12: a restart applied a two-step
expand/contract pair 0.26s apart, skipping the soak that was meant to sit
between them. Any migration you do not want applied yet must not be in the tree
when you restart.

> This is the opposite of production, which sets `SKIP_DB_SYNC=1` and applies
> nothing on boot. Do not carry an assumption from one box to the other.

## Deploy: mqtt-gateway

```bash
node --check <changed file>.js
pm2 restart gw-0 gw-1 gw-2 gw-3
```

The gateways hold live device connections. Restart them only when gateway code
actually changed — a docs-only commit in that folder is not a reason.

## Characters, Prompts And Banks

Prompts and question banks live in the database, not in the repo. The source of
truth is the `cheekocharactersystem` project's `agents/` directory; the pack
under `database-import-pack/` mirrors it for import.

```bash
cd /root/xiaozhi-esp32-server/main/manager-api-node
node scripts/install-character-pack.js <pack-dir>            # dry run
node scripts/install-character-pack.js <pack-dir> --apply
node scripts/create-missing-character-rows.js <pack-dir>     # only if rows are absent
```

The installer refuses a database it does not recognise (`EXPECTED_DB`). No
restart is needed afterwards: templates are not cached, and the worker
regenerates the workspace from `manager_db_prompt` at every session start.

**Worker code before prompts, never the reverse.** A prompt emitting a MEMO
`type=` the worker does not score records no verdicts at all — the child plays a
full game and nothing is saved. The scorer accepts `daily_quiz`, `daily_riddle`
and `daily_math`, so shipping code first is always safe.

## Verify After Deployment

```bash
pm2 list
cd /root/xiaozhi-esp32-server/main/manager-api-node
node scripts/character-check.js <MAC>
```

`character-check.js` is the after-session check: it reports whether the persona
loaded, whether progress persisted, whether the no-repeat ledger grew, and
whether the scored banks advanced. Read-only, safe to run mid-session.

Session traces land in `/root/.picoclaw/workspace-*/trace/`. A trace with
`MessageCount: 1` and a 20–40s duration is a session that ended after the
greeting, usually a device disconnect rather than an agent fault.

## Rollback

- Worker: `git checkout -- <file>`, rebuild, `pm2 restart picoclaw-livekit`.
- Backend: `git checkout -- <file>`, `pm2 restart manager-api --update-env`.
- Prompts: re-run the installer from a known-good pack, or restore the JSON dump
  it writes to `backups/character-pack-<date>/` before every `--apply`.

## Common Problems

### `prisma migrate status` says clean but a column is missing

The `content_banks` migration uses `CREATE TABLE IF NOT EXISTS`, which no-ops
against an older-shaped table of the same name and still reports success. Found
2026-08-20: DB1 held a JSONB-shaped `story_bank` while the Prisma model expected
the flat `beat1_hook…beat6_ending` shape, so every Nani read threw.

Compare `information_schema.columns` against local rather than trusting migrate
status. `btrim()` will not help you spot whitespace drift — in Postgres it
strips spaces only, never newlines.

### `400 Bad Request` with an empty body from the manager API

The `.env` `SERVICE_SECRET_KEY=` line carries quoting and whitespace, so
`cut -d= -f2-` yields a 75-character value that is an invalid HTTP header. Node
rejects it at the parse layer, which looks nothing like a validation error.
Strip it first — the real key is 36 characters:

```bash
grep '^SERVICE_SECRET_KEY=' .env | cut -d= -f2- | tr -d "\"'" | tr -d '[:space:]'
```

### `go test ./pkg/livekit/` fails on the box

`libten_vad.so: cannot open shared object file` — a test-loader environment
issue only. The built binary loads it fine. Run tests locally.

### Privacy note

`detailed_trace_enabled=true` is still on, so children's conversation content is
written to logs in plaintext. Turn it off before this box carries real users.
