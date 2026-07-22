# SUB-13 launch runbook — fleet migration, comms & kill-switch

One page to execute on drill day and flip day. Human at the wheel; every
state-changing script defaults to dry-run. All commands run on the box hosting
manager-api (`cd .../manager-api-node`).

## 0 — Preconditions (all must be true before flip day)

- [ ] SUB-17 done: products live in both store consoles, prod `appl_`/`goog_` RC keys in the
  released app, RC webhook → prod URL, `REVENUECAT_WEBHOOK_AUTH` set in prod env
- [ ] App build with paywall (SUB-16) approved and live in both stores
- [ ] Real-store sandbox e2e re-runs done (SUB-16 purchase/restore, SUB-9 up/downgrade)
- [ ] Admin dashboard + the three alert channels (SUB-11) confirmed alive in prod
- [ ] DB backup / snapshot taken

## 1 — Kill-switch drill (before comms day; safe any time pre-launch)

Uses a team test device (e.g. `68:EE:8F:60:BC:00`), not a customer's.

1. Note the device's current row:
   `SELECT status, trial_ends_at FROM device_subscriptions WHERE mac_address='68:EE:8F:60:BC:00';`
2. Gate it: `UPDATE device_subscriptions SET status='lapsed' WHERE mac_address='68:EE:8F:60:BC:00';`
3. Enforcement ON: set `ENFORCEMENT_ENABLED=true` in `.env` → `pm2 restart manager-api --update-env`
4. Talk to the toy → expect the **gate clip**, not a conversation.
5. Kill switch: set `ENFORCEMENT_ENABLED=false` → `pm2 restart manager-api --update-env`
6. Talk to the toy again → expect a **normal session on the first attempt**. That's the drill pass.
7. Restore the row from step 1 and leave enforcement OFF.

Record the two timestamps (flip → allowed session); the revert budget is "one session attempt".

## 2 — Comms (T-2 or T-3 days)

Push (copy lives in the script; edit there if wording changes):

> **Cheeko plans are coming 🎉** — Your first month is on us — every Cheeko gets a free
> month of the Family plan at launch. Nothing to do today; pick a plan in the app when
> your free month ends.

```
node scripts/send-launch-announcement.js          # dry run: parent/token counts
node scripts/send-launch-announcement.js --apply  # send (once)
```

In-app: the paywall + trial banner (SUB-16/SUB-10) become the standing in-app announcement
the moment trials exist — no separate app release needed.

## 3 — Flip day

Order matters; the seed MUST complete before the flag flips (missing row == lapsed once
enforcement is on).

1. Deploy latest backend (schema already live).
2. Seed: `node scripts/seed-launch-trials.js` (dry run — sanity-check counts)
3. `node scripts/seed-launch-trials.js --apply` — **must print "Coverage OK"**. Non-zero
   exit = STOP, do not flip; fix the listed MACs (admin re-grant, SUB-11) and re-run
   (idempotent).
4. Flip: `ENFORCEMENT_ENABLED=true` in `.env` → `pm2 restart manager-api --update-env`
5. Smoke: one team device with a trial row talks normally; the drill device (if left
   lapsed) gets the gate clip.

## 4 — Launch-day watch (first 24 h)

- Funnel dashboard (SUB-11): verdicts, gates fired, paywall opens, purchases
- Alert channels: **fail-open alerts must be zero or individually explained**; billing-issue
  spike alert; ops channel
- RC webhook deliveries (RC dashboard → prod endpoint, no retries piling up)
- Support inbox for false-lockout reports — any confirmed false lockout ⇒ kill switch
  (section 1, steps 5–6), diagnose offline, re-flip when fixed.

## 5 — Day-30 validations (schedule the calendar entry on flip day)

- Trial→paid conversion rate vs the costing-sheet assumption
- Bucket-consumption distribution (are the Family limits sized right — spec §8)
- Pricing review with real attach data (₹199/499/999 and the 15% store cut)
