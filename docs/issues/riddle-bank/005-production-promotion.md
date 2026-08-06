# 005 — Production promotion

## Parent

`docs/issues/riddle-bank/000-design.md`

## What to build

Promote Riddler to production. **HITL — a human runs this.** An agent may prepare the SQL,
the CSV import command and the rollout plan, but must not execute against production.

Order matters: schema, then content, then API, then worker, then the character row. Each
step before the last is inert — the worker sends no `character` param until it is rolled
out, and no session reaches Riddler until the `ai_agent_template` row exists. That ordering
means every step is independently reversible and nothing half-deployed is user-visible.

Traps specific to this environment, all previously observed:

- **Production never used `prisma migrate`.** `_prisma_migrations` does not exist and all
  migrations report "pending". Running `migrate deploy` would replay everything. Apply the
  migration SQL directly instead.
- Run `prisma generate` after the schema change or `prisma.riddle_question` is `undefined`
  and the importer fails on every row.
- Plain `node` scripts do not load `.env` — `set -a && . ./.env && set +a` first, or the
  connection string prints empty.
- `log.Printf` goes to stderr; under pm2 that is `*-error.log`, not `-out.log`.
- `agent_code` is the join key. An UPDATE written against a display name matches zero rows
  and succeeds silently.

## Acceptance criteria

- [ ] Migration SQL applied directly to production; both riddle tables exist
- [ ] `prisma generate` run on the production deployment
- [ ] All 90 riddles imported; import is idempotent on a second run
- [ ] API rolled out; `/quiz/next-questions` with no `character` param returns quiz data
      exactly as before
- [ ] Worker rolled out; a Quizzy session on production is verified unaffected **before**
      the Riddler character row is created
- [ ] `ai_agent_template` row created with `agent_code = 'riddle_master'`
- [ ] One real production session answers riddles end to end; rows land in
      `riddle_question_answer`
- [ ] `quiz_question_answer` gains no rows from that session
- [ ] Verified from `voice_session_messages` and DB rows, not from logs alone
- [ ] Rollback path written down before starting: which revision to roll the worker back to,
      and confirmation that leaving the tables in place is harmless

## Blocked by

- `docs/issues/riddle-bank/002-riddler-speaks.md`
- `docs/issues/riddle-bank/003-full-riddle-content.md`
