# 008 — Production promotion

**Type:** HITL · **Status:** open — prepared 2026-08-05, not executed
**Deploy boundary:** every step below is on production. Per `cheeko-deploy-boundaries`, these
are Rahul's to run. The 2026-08-03 EKS deploy was a one-time grant, not standing permission.

## Production shape (verified by read-only survey, 2026-08-05)

| | |
|---|---|
| Worker | EKS `picoclaw-eks`, ap-south-2, **namespace `picoclaw-dev` is prod**, 2 pods, HPA 2–10 |
| Services | `manager-api`, `manager-web`, `mqtt-gateway` on 139.59.7.72 / ota.cheekoai.in |
| Database | DigitalOcean managed Postgres (`db-postgresql-blr1-93302`), **not** Supabase |
| Quiz schema | **absent** — no `quiz*` tables, migration unapplied |
| Quizzy row | present as `quiz_master`, `cut_over=false` (no `{{QUIZ_QUESTIONS}}` yet) |
| Prompt sizes | prod `system_prompt=8312 greeting_prompt=1799 soul=5492`; dev `12439 / 2428 / 5492` |

`soul` is byte-identical on both, so the cutover writes only two columns.

## The one remaining gate

**Band 3-5 needs end-to-end session coverage before promotion.**

Of 10 profiled prod devices, **nine are band 3-5 and one is 6-8**. All of this week's live
validation — every fix, every regression, Kishore's whole history — ran on band 6-8. The bank
that serves nine of ten real children is the one with the least evidence behind it.

Partial coverage exists: `14:C1:9F:D6:44:F4` (Hitansh, band 3-5) cleared level 1 cleanly on
2026-08-05 and started level 2. Level 3 for band 3-5 was authored the same day and **has never
been played by anyone**.

To close it, on the dev box:

- [ ] Run Hitansh's device through 3-5 level 2 to completion
- [ ] Confirm the level-2 → level-3 transition serves ids from the new 3-5 level 3
- [ ] Confirm a `[QUIZ] level complete: kid=… 3-5 level 2` milestone writes
- [ ] Read the transcript, not just the rows — 3-5 questions are answered by younger children
      whose speech Sarvam mishears more often; the homophone rule has only been tested on 6-8

(The 45 unprofiled prod devices are test units Rahul is removing, so the band-default question
they raised is moot. `DEFAULT_AGE_BAND` stays `6-8`.)

## Content ceiling

Three levels per band = three days of play before champion replay repeats forever. Not a
deploy blocker, but the first engaged child hits it within a week. Grow **band 3-5 first**.

## Runbook

Order is load-bearing: each step's failure mode is the next step's silent bug.

### Prep
1. Merge both branches to `main` (both are 0 commits behind — clean fast-forward) and tag
   `quiz-bank-v1` in each repo. Deploy the tag; it is the rollback anchor.
   - `picoclaw`: 24 commits ahead · `cheeko-backend`: 12 commits ahead
2. Snapshot the prod Quizzy row before touching it:
   `DATABASE_URL="<prod>" node /tmp/quizzy_prompt_io.js dump prod-quizzy-before.json`
3. Rotate credentials first — the DB1 string, the prod DB string and an OpenRouter key have
   all been pasted into transcripts.

### Database (before any service)
4. Apply `20260804000000_add_quiz_question_bank`. Additive only: new tables, CHECK
   constraints, `ON DELETE RESTRICT`. No existing table is touched.
5. Seed: `npm run import:quiz-bank prisma/seed-data/quiz-bank-all.csv` → 90 rows, idempotent
   by `code`. Verify 3 bands × 3 levels × 10.

### Services
6. **`manager-api` first.** The worker calls `/quiz/*`; deploy the worker first and every
   session silently falls back to free chat with no error anyone will notice.
7. `manager-web` (rebuild `dist/`), then `mqtt-gateway` — the gateway restart drops every
   connected device, so pick a quiet window.
8. **Worker to EKS last** (`deploy/README.md`, recipe verified 2026-08-03):
   ECR login → `docker build -f Dockerfile.eks` → push → digest → pin in
   `deploy/k8s/livekit-deployment.yaml` → `kubectl diff` (**must show only the image line**)
   → apply → `rollout status`. ~2 min, zero-downtime.
   - Verify the code shipped — the image has no `strings`, and a `strings` miss returns a
     misleading 0:
     `docker run --entrypoint sh <img> -c "grep -ac 'routed to upstream' /usr/local/bin/picoclaw-livekit"`
   - Nothing to set for OpenRouter routing. `OPENROUTER_PROVIDER_ORDER` was deleted
     outright on 2026-08-07 — the worker always asks for `sort=latency`. If an old
     manifest or shell still exports it, it is inert and can be dropped.
   - CVE baseline: 1 CRITICAL + 3 HIGH, identical to the running image. Compare against the
     live digest's scan rather than blocking on the raw count.

### Cutover last
9. Load dev's Quizzy prompt onto prod — **only after** the bank is seeded and `/quiz/*` is
   live, or `{{QUIZ_QUESTIONS}}` renders "bank unavailable" and Quizzy refuses to quiz.
   Source dump: `quizzy_prompt_source_20260805.json` (`system_prompt=12439`,
   `greeting_prompt=2428`, `soul` unchanged).

### Verify
10. One session on a prod device that has a kid profile — ideally a band 3-5 one, since that
    is the real population. Expect: `level=N questions=10` in the manager log,
    `routed to upstream provider "Crusoe"` (stderr log, not stdout), `tools=0` for Quizzy,
    answer rows landing, `quiz_bank.md` dated today.

## Rollback

| Layer | How | Speed |
|---|---|---|
| Prompt | re-load the step-2 snapshot | seconds, no deploy — **fastest kill switch** |
| Worker | `kubectl -n picoclaw-dev rollout undo deployment/picoclaw-livekit` | ~2 min |
| Services | redeploy the previous tag | minutes |
| Schema | leave it — additive, nothing else reads those tables | n/a |

The prompt rollback reverts Quizzy to pre-bank behaviour without touching a single service.
If anything looks wrong in the first session, use that before unwinding deploys.

## Known gaps shipping with this release

- **[007](007-api-down-free-chat-path.md) unverified** — what a child hears when the Manager
  API is down has never been exercised.
- **Quizzy still registers `tools=2`** (cron and MCP paths not covered by the character gate).
  Harmless — `tool_call_count` is 0 on every observed turn — but the request body still
  contradicts a prompt that says never to call tools.
- **Lost-verdict reordering.** A turn that dies after the model judges but before the MEMO is
  parsed loses that verdict; the question is re-served later, out of order. Self-heals because
  the server recomputes Cleared from the log, but the child hears an apology and a repeat. Fix
  proposed (rewrite `quiz_bank.md` after each answer, naming the next question as fact) but
  unbuilt. Seen live on 2026-08-05 — see 006.
- **Provider pin has ~1 day of single-device validation.** `Crusoe,CoreWeave`, chosen after
  `DeepInfra` matched an fp8 endpoint at 6 tok/s and produced 48-second timeouts.
