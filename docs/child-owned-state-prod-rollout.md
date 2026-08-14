# Child-owned state — what to watch when this reaches prod

> Everything below shipped to the **dev box only** (2026-08-13). Prod and EKS are
> untouched. Detail lives in `docs/issues/child-owned-state/` and
> `docs/adr/0008-the-child-owns-learning-state.md`; this is the short list for the
> rollout.
>
> **This deploy is not alone.** The chat-history attribution work sits on the same
> branch and ships in the same change window — read
> `docs/chat-history-prod-rollout.md` alongside this. Where the two overlap, that
> document owns the chat-history half and this one owns the migrations.

## What ships

| repo | HEAD | what it contains |
|---|---|---|
| `picoclaw` | `394839b` | workspace directory named from the Manager's owner key; the chat-history worker changes |
| `cheeko-backend` | `8605799f` | nine migrations; quiz card endpoint; kid identity and ownership fixes; unbind is a real handover |

**Unlike the chat-history work, this half carries schema changes.** Nine
migrations, `20260812020000` through `20260813000000`. That single difference
drives most of what follows.

## Before you deploy anything: three questions about prod's migration state

`server.js` runs `runPrismaMigrations()` on boot. On the dev box that meant a
restart applied every pending migration in the tree at once. Prod may not behave
the same way, and **guessing here is how you replay 37 migrations against a live
database**.

1. **Does prod have a `_prisma_migrations` table, and what is in it?** There is a
   standing note that prod was never built with `prisma migrate`. If that is true,
   `migrate deploy` will consider *every* migration pending. Check before the
   deploy, not during it.
2. **Does prod already have the `parent_profile` consent columns?** DB1 did, which
   made `20260813000000` a no-op there. The migration is `IF NOT EXISTS` either
   way, so this is informational — but it tells you whether prod shares DB1's
   history or the older MySQL-migrated shape.
3. **Does `imagine_image` exist?** It did not on DB1; `20260812070000` creates it.

Run the same three read-only checks the dev promotion used — they are recorded in
`docs/issues/child-owned-state/010-dev-box-promotion.md` with DB1's answers, so
you have a baseline to compare against.

## The migration that can stop the deploy, on purpose

`20260812040000_workspace_memory_owner_key` is the one to read before running.

It assigns `owner_key` to the workspace and memory tables. The original version
stamped every row on a paired device with that device's **current** child,
ignoring the `kid_id` the row already carried — and then resolved the collisions
that mis-attribution created with `DELETE`. On DB1 that would have relabelled 82
of two children's memory documents as a third child's and deleted the ones that
collided on `document_key`.

The version that ships does three things differently:

- rows that already know their child keep it;
- rows that do not are claimed for the device's current child **only where no
  sibling table names a different child**, computed by the migration itself into a
  temp table;
- the collapse is now a `RAISE EXCEPTION` rather than a `DELETE`.

**So on prod this migration may fail, and that is the design working.** A failure
means a device has served several children in a way the guard did not anticipate.
Do not "fix" it by reverting to the deleting version. Read the exception, find the
device, and decide what its unattributed rows should be — the same decision
recorded for DB1 in ticket 010.

The precondition used to be a comment citing a survey run by hand against a
different database. It was true there and false on DB1. That is why it is now
executable.

## Deploy order

**manager-api first, then the worker**, same change window.

The worker names its local workspace directory from an `ownerKey` the Manager
returns on lock acquire. A worker deployed first simply does not receive the field
and falls back to naming from room metadata — the old behaviour, which is wrong
but not harmful. A Manager deployed first is strictly correct. So the ordering is
a preference here, not the hard constraint it is for chat-history, where shipping
the worker alone actively makes things worse.

Prod worker is on EKS (`ap-south-2`, namespace `picoclaw-dev`) — an image build and
rollout, not the pm2 recipe used on dev. **The dev build was the first time this
binary has ever linked anywhere**; the local Windows build fails on libolm cgo, so
every slice before the promotion shipped on `go vet` plus package tests only.
Budget for a build that has never been done in your pipeline.

## What behaves differently after the deploy

1. **State follows the child, not the toy.** A child moved to a replacement toy
   takes memory, workspace, quiz and riddle progress, chat history and the imagine
   gallery with them; the toy left behind reads empty. This is the point of the
   phase — but it means a parent who swaps toys sees history "move", and a support
   team looking at a MAC will no longer find that child's data under it.

2. **Unbind now sweeps what the toy wrote while unpaired.** `mac:`-keyed workspace,
   memory, chunks, quiz/riddle answers and `imagine_image` rows are deleted when a
   device is released. Without this, the next family's pairing runs
   `adoptUnattributedRows` and hands those rows to *their* child. `kid:` rows are
   never touched. **Imagine S3 objects are deliberately left in place**, so a
   mistaken unbind costs a gallery listing rather than the pictures.

3. **`hardDelete` on unbind is now super-admin only.** The parent-facing route
   ignores the flag. Clearing `user_id` already removes the toy from the parent's
   list — `listDevices` filters on it — and the delete only destroyed the
   assignment history and the MAC-keyed analytics on top of that. **If any client
   depends on the row disappearing, it will now find the row still there,
   unowned.**

4. **`POST /api/mobile/kids` is now create-or-get**, matching
   `(user_id, lower(name), birth_date)`. A repeat activation returns the existing
   child instead of a new one, so the returned id may be one the app has seen
   before. A child with no birth date is always created. Prod almost certainly has
   duplicates already — see below.

5. **`PUT /api/mobile/kids/:id` returns 404 for a child the caller does not own.**
   It previously updated any child in the database by id. Anything relying on the
   old permissiveness — including a client sending a stale id — now gets a 404
   where it used to get a 200.

6. **`GET /api/mobile/kids` is ordered and enriched.** Paired children first by
   their toy's `last_connected_at`, then by profile age. Each child gains
   `device_mac` / `deviceMac` and `is_paired` / `isPaired`. It also accepts
   `?mac=`, which returns only that toy's child — `[]` for an unpaired toy, `404`
   for a toy the caller does not own. Additive, so the current app keeps working;
   the ordering changes which child `loadedKids.first` picks, which is the point.

7. **`GET /api/mobile/quiz/progress` exists for the first time.** The app has
   always called it and always got a 404, which `getQuizProgress` swallows as "no
   data" — so the home quiz card has rendered empty on every deployment since it
   shipped. After this deploy it renders real data. Expect the screen to change
   appearance for every user at once.

8. **Worker workspace directories become `workspace-kid-<id>`** for paired devices.
   On EKS the workspace is ephemeral so there is likely nothing to migrate — verify
   that for the prod deployment rather than assuming it.

## Duplicate child profiles — a separate, manual step

Same shape as the duplicate character rows in the chat-history rollout, and the
same reasoning: the fix stops new ones, the existing ones need a script.

Every toy activation walked the parent through "who is this for" and created a
profile, so accounts accumulate identical children. Dev held two `Kishore`s and
two `Rahul`s under one account, each pair identical on name and birth date.

**This one is not cosmetic.** Under child-owned state a duplicate profile is an
*empty history*, and the toy gets paired to it — one dev account had 67 memory
documents and 71 voice sessions stranded under the older profile while the toy
pointed at the new, empty one.

```bash
node scripts/merge-duplicate-kid-profiles.js            # dry run — read it first
node scripts/merge-duplicate-kid-profiles.js --apply    # writes a backup json
```

It needs `DATABASE_URL` and does not load `.env` itself — `node -r dotenv/config`
is the least painful way. It keeps the **newest** row of each pair and moves the
older one's history into it, repointing `owner_key` rows as well as `kid_id`.
Collisions are dropped to a `.discarded.json`; on dev that was one row. Read both
files before assuming a clean run.

## Known-open, going in with your eyes open

- **Auto-pairing did not happen.** Ticket 010 expects 23 of 24 devices paired after
  slice 001; DB1 sits at 6 of 24. The migrations record existing pairings but
  nothing pairs the devices whose owner has exactly one child. Either that is a
  bind-time code path or the promotion is missing a backfill — **unresolved**, and
  prod will land in the same state.
- **`POST /toy/device/unbind-open` is unauthenticated by design.** Anyone who can
  reach the port can unbind any device by id, and after this deploy that call also
  sweeps `mac:` rows — so it is more destructive than it was. Put it behind the
  service key before this reaches an internet-facing host.
- **`config.service.js createAndAssignChildProfile`** is named in the merge script
  as another path that creates child profiles. Only `mobile.service.createKid` was
  fixed. **Unverified** — if that path is live, prod can still mint duplicates
  after the deploy.
- **`device_memory_documents` still carries a pre-`owner_key` unique constraint**
  (`child-owned-state/011`). Shared with the chat-history rollout; see that
  document. The migration is not written.
- **Three app-side bugs remain**, documented in
  `docs/mobile-active-child-selection.md`. All the same pattern — the app inferring
  state from an incidental field instead of the one the API publishes:
  `loadedKids.first` for the active child, `agentId.isEmpty` as "already removed"
  (which strands a device in the list with no way to remove it), and falling back
  to a sibling when a toy has no child yet. **The server changes make the defaults
  right; they cannot make the selection right.**
- **`backfill-imagine-images.js` has never run anywhere.** Until it does, the
  gallery shows only images uploaded since the deploy.
- **`detailed_trace_enabled` is still on**, still writing children's speech to
  plaintext logs.

## Verify after the prod deploy

```sql
-- every workspace/memory row has an owner, and the split looks sane
SELECT split_part(owner_key,':',1) AS ns, count(*) FROM device_memory_documents GROUP BY 1;
SELECT count(*) FROM device_workspace_artifacts WHERE owner_key IS NULL;  -- must be 0

-- no row was mis-attributed: a device that served several children must not have
-- all its history under one of them
SELECT d.mac_address, m.kid_id, count(*) FROM ai_device d
JOIN device_memory_documents m ON lower(m.mac_address) = lower(d.mac_address)
WHERE m.kid_id IS NOT NULL AND d.kid_id IS NOT NULL AND m.kid_id <> d.kid_id
GROUP BY 1,2;

-- duplicates, before and after the merge
SELECT user_id, lower(name), birth_date, count(*) FROM kid_profile
GROUP BY 1,2,3 HAVING count(*) > 1;

-- pairing coverage, to compare against ticket 010's expectation
SELECT count(*) FILTER (WHERE kid_id IS NOT NULL) AS paired, count(*) AS total FROM ai_device;
```

Then the live checks, which are the ones that have historically found things the
SQL did not:

- **One Quizzy and one Riddler session on a paired toy.** Answers carry the right
  `kid_id`, the injected prompt shows the right level, and the two banks stay
  separate.
- **A toy re-paired to a second child**: the new child gets a clean workspace,
  empty progress and an empty gallery, and the first child's rows are still intact
  and still theirs.
- **The first child re-paired to a different toy**: memory, workspace, progress,
  chat history and gallery all recover.
- **Worker log shows `workspace=…/workspace-kid-<id>`** for a paired device, and
  the line `Workspace identity resolved from manager owner key` on any dispatch
  whose room metadata omits `kid_id` — which is every quiz-character dispatch.

## Rollback

**The code rolls back; the schema does not.**

Reverting both repos to the previous image and commit restores the old behaviour,
and the added columns are ignored by the old code — with two exceptions that make
a blind revert unsafe:

- `owner_key` is `NOT NULL` on three tables with unique indexes over it. Old code
  does not write it, so **inserts into those tables will fail after a code-only
  rollback**. Rolling back means dropping those constraints too.
- The unbind sweep and any applied merge are permanent. Rows deleted on a handover
  do not come back, and merged profiles stay merged.

Rows written with correct attribution stay correct after a rollback, which is
harmless. Plan the rollback as a schema decision, not a redeploy.
