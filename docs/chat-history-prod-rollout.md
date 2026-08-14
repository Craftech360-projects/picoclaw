# Chat-history attribution — what to watch when this reaches prod

> Everything below shipped to the **dev box only** (2026-08-13). Prod and EKS are
> untouched. Detail lives in `docs/issues/chat-history-attribution/` and
> `docs/api/chat-history-api-changes.md`; this is the short list for the rollout.

## What ships

| repo | HEAD | what it contains |
|---|---|---|
| `picoclaw` | `99e6be7` | worker sends the speaking character; per-character transcript; log fixes |
| `cheeko-backend` | `8605799f` | kid-scoped read endpoints; character dedupe on create; bind 4xx; merge script |

**No Prisma migration in either.** Nothing here needs `prisma generate` or a
schema change. Confirm that again at rollout time — `server.js` runs
`runPrismaMigrations()` on boot, so *any* unapplied migration sitting in the tree
gets applied by the restart, whether or not it belongs to this work.

## Deploy order, and the one thing that must not be split

**Ship manager-api and the worker together, manager-api first.**

The worker now sends the character it is running. The Manager's device bootstrap
had to learn to scope its history reads to that character. If the worker goes out
alone, a Quizzy session bootstraps and gets **Cheeko's** recent messages back —
worse than the bug being fixed. On dev they went out inside the same hour; on prod
they should go out inside the same change window.

Prod worker is on EKS (`ap-south-2`, namespace `picoclaw-dev`), so it is an image
build + rollout, not the pm2 recipe used on dev. Prod manager-api is its own host
and its own database — the dev cleanup below has **not** touched it.

## Five things that behave differently after the deploy

1. **Attribution has a cutover.** Sessions before the deploy are attributed to the
   device's default character; sessions after carry the real one. Nothing on a
   message row records who spoke, so history before the cutover **cannot be
   repaired**. Record the prod cutover timestamp — parent-facing screens may want
   to caption older sessions.

2. **Each character gets its own transcript file, and the old shared one is
   deleted** on the first session after deploy. On EKS the workspace is ephemeral,
   so there is likely nothing to lose — verify that for the prod deployment before
   assuming it. If prod workspaces *are* persisted, snapshot them first; the
   deletion is one-way. `memory/MEMORY.md` session summaries survive either way,
   which is what keeps cross-character continuity.

3. **`POST /api/mobile/agents` is now create-or-get.** A repeat call returns the
   account's existing character row instead of a new one. The app's activation
   flow creates an agent per attempt, so this is what stops prod accumulating
   duplicates — but it means the returned id may be one the app has seen before.

4. **Bind failures return 400/404/409 instead of 500.** Any app or dashboard
   branching on "500 means bad code" needs checking.

5. **`resolveProgressScope` now honours a `kidId` it previously ignored.** Six
   existing progress endpoints pass query options straight through. If the app
   sends `kidId` to those screens, they will now scope by that child rather than
   ignoring it, and an unowned `kidId` returns 404 instead of being silently
   dropped. No test asserted the old behaviour, so the suite cannot catch this —
   check it against the real app.

## Duplicate character rows — a separate, manual step

Prod almost certainly has them: the app creates one agent per toy activation and
leaves one behind on every failed bind. Dev held 22 extra rows across 4 accounts,
one account with fourteen `Cheeko`s.

The read merges duplicates, so **the parent-facing screens are correct without
this step**. Cleaning up is hygiene, and it is deliberately manual:

```bash
node scripts/merge-duplicate-agents.js            # dry run — read it first
node scripts/merge-duplicate-agents.js --apply    # writes an audit json
```

It repoints all seven `agent_id` tables plus `ai_device` **before** deleting,
because the FKs are `onDelete: SetNull` and deleting first would convert history
into orphans. On dev it moved 3299 references with the NULL-agent counts unchanged
at 24 and 145 — those two numbers are the assertion that the ordering worked.
Capture them before and after on prod.

Deleting an agent row cannot be undone from the database alone. The audit file it
writes is the only record of what moved where.

## Known-open, going in with your eyes open

- **`device_memory_documents` still carries a pre-`owner_key` unique constraint**
  (`child-owned-state/011`). Any device whose pairing changed after a document was
  written can never update its rolling memory again — 89 such rows on dev, every
  session ending in a 400 on the summary PUT. **Unfixed; the migration is not
  written.** Check the equivalent count on prod before deploying, because the
  failure is noisy.
- **Cross-account history** — a toy activated under one account and later paired
  to another account's child keeps the old account's character on its sessions. 84
  sessions on dev. Reported by the merge script, never moved: deciding who owns a
  child's past is a product call.
- **Bootstrap still resolves the persona from the device default**, so a Quizzy
  bootstrap returns Quizzy's history beside Cheeko's prompt. Harmless today — the
  worker takes its persona from room metadata — but it is one response
  contradicting itself.
- **Per-character settings are now shared across siblings.** `ai_agent` carries
  language and voice, and there is one row per character per account, so changing
  kid A's Cheeko language changes kid B's.

## Verify after the prod deploy

```sql
-- expect several characters, not one; before the fix this was 100% the default
SELECT a.agent_name, count(*) FROM voice_session_messages m
JOIN ai_agent a ON a.id = m.agent_id
WHERE m.created_at > now() - interval '1 day' GROUP BY 1 ORDER BY 2 DESC;

-- must not grow after a merge: history that lost its character
SELECT count(*) FROM voice_session_messages WHERE agent_id IS NULL;
SELECT count(*) FROM voice_sessions        WHERE agent_id IS NULL;

-- every device still names a character that exists
SELECT count(*) FROM ai_device d LEFT JOIN ai_agent a ON a.id = d.agent_id
WHERE d.agent_id IS NOT NULL AND a.id IS NULL;
```

Then one live session per character on a **paired** toy: the session row, its
message rows and `kid_id` should agree, and the worker log should show
`character_id=<uuid>`. A non-UUID `character_id` is rejected by design and falls
back to the old behaviour — silently, so the SQL above is how you would notice.

## Rollback

Both changes are code-only, so rolling back is a redeploy of the previous image
and commit. Two things do **not** roll back: rows written with correct attribution
stay correct (harmless), and any merge already applied is permanent.
