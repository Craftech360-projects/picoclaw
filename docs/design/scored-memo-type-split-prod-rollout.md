# Prod rollout: per-character MEMO state types

Rollout plan for the change that gives each scored bank its own MEMO `type=`
label. Written 2026-08-21, after the dev-box release. **Nothing here has been
executed against production.**

Production deploys require an explicit per-deploy grant from Rahul — see the
standing rule in `cheeko-deploy-boundaries`. This document is the plan, not
permission.

## What ships

| repo | commit | target |
|---|---|---|
| picoclaw | `ec14877` | EKS `picoclaw-eks` / ns `picoclaw-dev` (this IS prod) |
| cheeko-character-system | `1bbc725` | prod manager DB (`ai_agent_template` rows) |
| cheeko-backend | `e3205553` | `139.59.7.72` (script only, no service change) |

## Why it has to ship at all

The MEMO `type=` label is three things at once: the scoreboard's filename, its
`kid_character_state.state_type`, and — until `ec14877` — the single literal the
scorer matched. Quizzy, Bujho and Ginti all emitted `daily_quiz`, so the three
shared one row per child. Whoever played last overwrote the others' daily score,
and a restored scoreboard could hand one bank another bank's `awaiting=`
question id.

`WriteQuizBankState` cleared the shared file on a bank switch, but that guard
reads `quiz_bank.md`, which is never persisted to the manager DB. A workspace
that starts empty while DB state is restored skips the clear entirely — which is
exactly the EKS shape, where the workspace is an `emptyDir` and every pod starts
cold. **Prod is more exposed to this bug than the dev box, not less.**

## The ordering rule

> Worker code first. Prompts second. Never the reverse.

The worker accepts `daily_quiz`, `daily_riddle` and `daily_math`. A prompt
emitting a label the scorer rejects records **no verdicts at all** — the child
plays a full game and nothing is scored. This was demonstrated accidentally on
2026-08-21: a prompt-only change to Ginti silently disabled its scoring for
about four minutes.

Because the accept-set keeps `daily_quiz`, Phase 1 is a no-op in behaviour and
Phase 2 is independently revertible. Any pause between phases is safe.

## Phase 0 — read-only prod audit

Prod state is **unknown**. The dev box carried schema drift that
`prisma migrate status` reported as clean (`cheeko-db1-schema-drift`), and the
character prompts there turned out to be a stale export. Assume neither has been
checked in prod until it has.

1. **Which database does prod use?** The EKS worker pulls personas from a
   Manager API URL held in a k8s Secret; reading Secrets and grepping logs for
   URLs is blocked by the permission classifier, so **Rahul must confirm** which
   manager API the EKS pods talk to and which Postgres that manager writes.
   Nothing else in this plan can start until that is settled — a prompt write
   aimed at the wrong database is the worst outcome available here.
2. **Roster check.** `SELECT agent_code, agent_name FROM ai_agent_template`.
   Confirm all 11 codes exist and that `riddle_master`'s `agent_name` is
   `Bujho`, not `Riddler` — `liveKitToollessCharacters` matches on the display
   name, so a stale name hands the character tools its prompt forbids.
3. **Prompt drift.** Hash each row's `system_prompt` / `soul` /
   `greeting_prompt` (whitespace-stripped) against
   `cheekocharactersystem/agents/`. Expect wide drift: prod has almost certainly
   never received the newer roster. Decide explicitly whether Phase 2 ships only
   Bujho + Ginti or the full roster.
4. **Content-table shapes.** Compare `information_schema.columns` for
   `story_bank`, `spell_bank`, `joke_bank`, `why_bank`, `word_bank` against
   local. The `CREATE TABLE IF NOT EXISTS` in `20260820020000_content_banks`
   no-ops against an older-shaped table and still reports success.
5. **In-flight state.** `SELECT state_type, character, count(*) FROM
   kid_character_state GROUP BY 1,2`. Count how many children hold a live
   `daily_quiz` row so the transition cost below is a number, not a guess.

## Phase 1 — worker to EKS

Recipe verified 2026-08-03, ~10 min end to end (`picoclaw-eks-prod-deploy`):

1. ECR login → `docker build -f Dockerfile.eks -t <repo>:<date-tag> .` → push.
2. Grab the digest, pin it in `deploy/k8s/livekit-deployment.yaml`.
3. **Verify the new code is actually in the image.** The image has no `strings`:
   `docker run --entrypoint sh <img> -c "grep -ac 'daily_riddle' /usr/local/bin/picoclaw-livekit"`
   A non-zero count proves `ec14877` is in the binary. (A `strings` miss returns
   a misleading 0.)
4. `kubectl apply --dry-run=server` + `kubectl diff` — **should show only the
   image line**. Anything else means the manifest drifted; stop and investigate.
5. Apply → `kubectl -n picoclaw-dev rollout status deployment/picoclaw-livekit`.
   Zero-downtime (`maxUnavailable=0`, 900s grace), ~2 min for 2 pods.

CVE note: the base image carries 1 CRITICAL + 3 HIGH (glibc CVE-2026-5450,
sqlite3), identical to the running image. Compare against the live digest's scan
rather than blocking on the raw count.

**Soak here.** Confirm existing `daily_quiz` scoring still works in prod before
touching any prompt. This phase changes no behaviour, so a long soak costs
nothing.

## Phase 2 — prompts to the prod DB

Only after Phase 1 has soaked.

1. Dump every current prompt row to a timestamped JSON file first. This is the
   revert path and it is not optional.
2. Write `riddle_master` and `math_master` (plus whatever else Phase 0 step 3
   decided) from `cheekocharactersystem/agents/`, with **no transform** — the
   installer's `daily_math`→`daily_quiz` rewrite is gone as of `e3205553`.
3. Assert before writing: payload contains no `daily_quiz` for those two, and
   does contain `daily_riddle` / `daily_math`.
4. Re-read and hash-compare every written row against source.
5. No manager-api restart. Templates are not cached, and a restart runs every
   pending prisma migration in the tree in one go (verified on the dev box
   2026-08-12) — confirm separately that prod's migration tree holds nothing
   unwanted before any restart happens for other reasons.

## Phase 3 — installer script to 139.59.7.72

`e3205553` empties the `TRANSFORM` map. Ship the file, `node --check` it, done —
no service restart, the script only runs when invoked by hand. Skipping this
phase does not break anything today; it just means the next pack install there
would reintroduce the shared label.

## Verification

Per character, after a real session on a prod device:

```
node scripts/character-check.js <MAC>
```

Success looks like separate `daily_math` / `daily_riddle` / `daily_quiz` rows
under CURRENT STATE, and a non-zero count in SCORED BANKS for the bank just
played. A `daily_math` row with `math today 0/10` means the MEMO persisted but
the verdict was rejected — that is the Phase-1-didn't-land signature.

## Transition cost (accept before shipping Phase 2)

Any child holding same-day Ginti or Bujho progress under the shared `daily_quiz`
row starts that character fresh on their next session, because the character now
looks for a label that has no row yet. It self-heals at the date rollover, and
**no answer-log data is affected** — real progress lives in the per-bank answer
log, not the scoreboard. Phase 0 step 5 turns this into a headcount. Shipping
Phase 2 late in the day minimises it.

## Rollback

- **Phase 2:** re-write the dumped JSON back into the three prompt columns, or
  just put `daily_quiz` back in Bujho's prompt. The worker still accepts it —
  that is the entire point of keeping all four labels in the accept-set.
- **Phase 1:** `kubectl -n picoclaw-dev rollout undo deployment/picoclaw-livekit`.
  Roll back Phase 2 first: an old worker plus new prompts is the one broken
  combination, because the old worker rejects `daily_riddle` and `daily_math`
  and scores nothing.
- **Phase 3:** `git checkout` the script.

## Known-unverified

- The per-bank `asked_keys` ledger split (`questionLedgerFor`) has never
  executed live. No ledger file existed in the dev workspace, because no MEMO
  there carried `asked_keys=`. It is covered by a unit test and nothing else —
  the collision it fixes is unobserved in both directions.
- Bujho has not been played end to end even on dev; only Ginti was
  (`math today 1/10`, 2026-08-21). Same code path, but untested is untested.
- Prod has never been inspected for any of this.
