---
status: open
assignee: rahul
---

# 007 — Production promotion

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

Promote per-age banks to production. **HITL — Rahul runs this.** Per
`cheeko-deploy-boundaries`, an agent may prepare the SQL, the verification queries and
the rollout order, but must not execute anything against production.

Same three steps as dev, same order, with a soak between each:

1. **Expand** — widen the CHECK, clone into ages 3–10, remap progress. Replay the remap
   immediately before step 2 to catch anything answered in between. Run late evening IST
   so the one-day `answered_today` double-count lands outside peak play.
2. **Migrate** — deploy the manager-api change. Rollback is a deploy revert.
3. **Contract** — retire the old rows and tighten the CHECK, only after live sessions
   across more than one age have been verified from the DB.

The Go worker needs no deploy — `age_band` is opaque to it end to end. Confirm that
rather than assume it.

Traps that have bitten this codebase before, all still live:

- Production never used `prisma migrate`; `_prisma_migrations` does not exist. Apply the
  SQL directly — `migrate deploy` would replay everything.
- Run `prisma generate` after any schema edit or the client serves stale models.
- `prisma.config.ts` overrides `DATABASE_URL` on the dev box — confirm the printed
  datasource host before concluding anything from a query.
- Plain `node` scripts do not load `.env`; `set -a && . ./.env && set +a` first.
- If quiz-bank `008-production-promotion.md` is still open, ride the same prod window.

## Acceptance criteria

- [ ] Backup taken before each step, and the restore path confirmed
- [ ] Post-expand: every (age × level × language) has exactly 10 active rows in both
      prod banks; no answer row deleted
- [ ] Post-expand and pre-deploy: live prod sessions behave exactly as before
- [ ] Post-deploy: a real child's session resolves to their single-age bank, verified
      from `voice_session_messages` and the answer rows — not from code review
- [ ] Devices with pre-migration progress resume at their correct level
- [ ] Post-contract: no old-band row active, CHECK rejects `'6-8'`, lifetime progress
      still readable
- [ ] Worker pods untouched throughout

## Blocked by

- `docs/issues/per-age-banks/005-retire-old-bands.md` — the full sequence must be proven
  on dev first
