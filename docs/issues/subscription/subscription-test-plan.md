# Subscription system A→Z test plan

2026-07-23. End-to-end verification matrix for the whole subscription implementation
(SUB-1…17), ordered as a life story: bind → trial → metering → gate → purchase → renew →
change → fail → lapse → launch. Each item: how to drive it, what must happen, where the
evidence is.

**Environments**
- **DEV** — otadev on the DO box (manager-api pm2 id 1, Supabase DB), app debug build with
  Test Store RC key, your phone + a team toy. Available today.
- **SANDBOX** — real App Store / Play Billing sandboxes. Needs SUB-17 (accounts, products,
  `appl_`/`goog_` keys). Items marked ⏳ can only be done here.
- Evidence sources: `pm2 logs manager-api` (`[SUBSCRIPTION]` / `[REVENUECAT]` tags),
  `device_subscriptions` + `subscription_events` tables, RC dashboard → Events, app UI,
  admin dashboard (SUB-11).

Test Store tip: its months are ~5 minutes, so renewal/expiry scenarios that take a month
in production run in minutes on DEV.

---

## A. Trial lifecycle (SUB-1/2) — DEV

1. ✅ *2026-07-23, `00:16:3E:AC:B5:38`* **Fresh bind grants one trial** — unbind a team toy, delete its `device_subscriptions`
   row (test device only!), re-bind from the app. Expect: row `status=trial`,
   `trial_used=true`, `trial_ends_at = +30d`; log `Trial granted`. *(row created same
   instant as bind; Family plan; 30d to the ms)*
2. **Re-bind never re-grants** — unbind/re-bind the same MAC (same or another account).
   Expect: row unchanged, `trial_started_at` original.
3. **Trial expiry gates** — `UPDATE ... SET trial_ends_at = now() - interval '1 day'` on the
   test row, enforcement ON (dev only). Expect: next session gets the gate clip; app gate
   screen shows re-subscribe CTA.

## B. Metering & buckets (SUB-3/4/5) — DEV

4. ✅ *2026-07-23* **Questions meter** — talk to the toy; watch `device_token_usage_session.message_count`
   rise. App usage panel (This month ring / Today meters) matches within one refresh.
   *(8 questions counted, app panel matched)*
5. ✅ *2026-07-23 (verdict layer)* **Daily bucket cutoff** — set the test row's plan to one with a tiny
   `daily_question_limit` (or temporarily lower the plan's limit), exceed it. Expect: verdict
   blocks with the bucket reason; resets at IST midnight. *(isolated with minute/monthly limits
   set high: verdict refused with `reason=daily_questions`. Full toy-clip e2e for this reason
   not re-run — the verdict→gateway→clip chain was already proven end-to-end via `daily_minutes`
   today; the gateway streams the clip on any `allowed=false`. IST-midnight reset still untested.)*
6. ✅ *2026-07-23 (verdict layer)* **Monthly bucket** — same via `monthly_question_limit`; anchor = `trial_started_at`
   (trial) / `current_period_start` (paid). Confirm the 80% push fires once (see G).
   *(isolated with daily/minute limits high: verdict refused with `reason=monthly_bucket_empty`;
   at 78.6% of the monthly limit it correctly still allowed. Gate proven at the verdict layer.
   Still open: the 80% bucket-alert PUSH (`maybeSendBucketAlert`) — needs an FCM token + crossing
   the threshold, folds into SUB-14 / section G.)*
7. ✅ *2026-07-23* **Mid-session minute cutoff (SUB-5)** — long session crossing the limit: heartbeat ends
   it mid-conversation, log shows the cutoff, next session gated. *(log: `Heartbeat cutoff …
   daily_minutes (8.4/8 min)` → `Interrupting active agent audio reason=end_prompt_farewell`
   → clean goodbye → session removed 5s later; next session gated. Two gotchas fixed: (a) dev
   agent had `USAGE_HEARTBEAT_INTERVAL=24h` in pm2 env → 5m; (b) farewell was streamed INSIDE
   the still-running response — fixed by interrupting the pipeline before the goodbye,
   commit `637ae989`, unit-tested)*

## C. Verdict & kill switch (SUB-1, spec §5) — DEV

8. ✅ *2026-07-23* **Fail-open default** — unset `ENFORCEMENT_ENABLED` → every device allowed; verdicts
   still logged. *(lapsed row + flag off ⇒ allowed; verdicts logged throughout)*
9. ✅ *2026-07-23* **Kill-switch drill** — runbook §1: gate test device → flag on → gate clip → flag off →
   normal session on first attempt. Record both timestamps. *(passed; also surfaced the
   stale-pm2-process trap — gateway must restart after deploys, now a runbook step)*
10. **Missing row = lapsed (enforcement on)** — device with no row is refused; admin
    re-grant (SUB-11) repairs it. This is the seed-script hazard the coverage gate exists for.

## D. Purchase flow (SUB-15/16) — DEV (re-proven; re-run ⏳ on sandbox)

11. **Paywall render** — no-plan device: 3 tiers, store prices, Family hero, trial banner
    when in trial.
12. **Buy** — purchase on Test Store: sheet → "Confirming with the store…" → celebration
    within one poll; DB row `active`, `store` set, `plan_id` correct; `INITIAL_PURCHASE` in
    ledger.
13. **Renewal anchors advance** — wait 2 Test Store months (~10 min): two `RENEWAL` events,
    `current_period_start/end` only ever move forward.
14. ✅ *2026-07-23 (service layer)* **Webhook idempotency** — re-POST a captured webhook body (same event id) with the auth
    header: 200, `duplicate`, no state change. *(replayed same event id → `duplicate`, no write)*
15. ✅ *2026-07-23 (service layer)* **Bad auth rejected** — same POST with wrong Authorization → 401, nothing ledgered.
    *(`verifyWebhookAuth('wrong')===false`, correct secret===true; HTTP-401 path not re-run)*
16. **Restore purchases** — ⏳ sandbox: reinstall app, Restore recovers the sub. (Test Store
    approximation already unit-covered.)
17. **Second-device ceiling** — ⏳ sandbox: second MAC on the same store account shows the
    honest ceiling copy, not a broken purchase.

## E. Plan change (SUB-9) — DEV webhook-level now, ⏳ sandbox for store semantics

18. ✅ *2026-07-23 (webhook level)* **Upgrade (webhook level)** — simulate: POST `PRODUCT_CHANGE` (ledgered only, state
    untouched) then `INITIAL_PURCHASE`/`RENEWAL` with the new `product_id` and fresh anchors
    → `plan_id` swaps, new limits on next verdict. *(PRODUCT_CHANGE left plan=family; RENEWAL
    with `cheeko_premium_monthly` swapped plan→premium and advanced anchors forward)*
19. ✅ *2026-07-23 (webhook part)* **Downgrade defers** — after a change commit, DB stays on the old plan until the
    period-end event; app shows the period-end notice, old limits hold (verdict check).
    *(webhook contract = same as 18: PRODUCT_CHANGE is ledger-only, no swap until the
    effective event — proven. App-side period-end notice already unit-tested in SUB-9.)*
20. **Abandoned sheet** — start a change in the app, cancel the sheet: flow returns to
    idle, no notice/error, no API refetch, no webhook, DB untouched.
21. ⏳ **Real upgrade** — sandbox: Apple immediate upgrade / Google `CHARGE_PRORATED_PRICE`;
    confirm the effective-time event lands within the app's ~20s poll (the one open timing
    assumption from review). If it routinely lags, lengthen `maxPolls` — copy already
    fails soft.
22. ⏳ **Real downgrade** — Apple period-end / Google `DEFERRED`; new plan applies only at
    rollover.

## F. Unhappy paths (SUB-7) — DEV (simulated webhooks), ⏳ sandbox billing-retry

23. ✅ *2026-07-23* **Billing issue → grace** — POST `BILLING_ISSUE`: status `grace`, `grace_until` = +3d or
    store window (later of the two); fix-payment push sent; device still allowed during grace.
    *(with realistic renewal-due timing: `grace`, `grace_until`=+3d. NB the guard only graces
    when the period is at/near its end — a mid-period billing issue is correctly ignored. Push
    send attempted but failed — see Firebase note under G.)*
24. ✅ *2026-07-23* **Expiration → lapsed** — POST `EXPIRATION` past period end: `lapsed`, plan-gate push;
    if `cancel_at_period_end` was set → relabelled `cancelled`, **no** push. *(both branches:
    plain → `lapsed`; with cancel flag → `cancelled`)*
25. ✅ *2026-07-23* **Cancel / uncancel** — `CANCELLATION` sets the flag (manage view shows "will not
    renew"); `UNCANCELLATION` clears it. *(flag set then cleared; status held active)*
26. ✅ *2026-07-23* **Refund via support** — `CANCELLATION` with `cancel_reason=CUSTOMER_SUPPORT`: immediate
    lapse + plan-gate push. *(→ `lapsed` immediately)*
27. ✅ *2026-07-23* **Stale/out-of-order events** — replay an old-period event after a newer purchase:
    ledgered, state untouched (anchor guard). *(old EXPIRATION → `ledgered`, row stayed active)*
28. **Nightly reconciliation** — run the SUB-7 reconciliation job manually; drifted row
    (hand-edit one) gets corrected from RC. *(job `src/jobs/rcReconciliation.js` exists; not yet run)*

## G. Pushes (SUB-10/14) — DEV, needs a real phone
> ✅ **UNBLOCKED 2026-07-23:** the dev Firebase service-account key (`46c4e29a80`) had been
> revoked in Google Cloud → every push failed with `invalid_grant: Invalid JWT Signature`
> (clock was synced; the app-repo copy was the same dead key). Fixed by generating a fresh
> key (`0b6574b743`, same `firebase-adminsdk-fbsvc@cheekoai…` SA) and installing it at
> `manager-api-node/cheekoai-firebase-adminsdk.json`. Verified: `sendPushNotification` → `true`
> (test push accepted by FCM). **Launch note:** confirm the PROD box isn't on the same revoked
> key — if it is, prod pushes are silently dead. Add an FCM live-send check to SUB-17.
29. **Trial reminders** — set `trial_ends_at` to +7d/+3d/today (per reminder schedule), run
    the cron: exactly one push per day-mark (`last_reminder_day` claims), deep-link opens
    the right screen.
30. **80% bucket alert** — push usage past 80% of monthly bucket: one push per period
    (`bucket_alert_sent_at` re-arms next period).
31. **Lifecycle pushes** — fix-payment (23) and plan-gate (24/26) arrive on the phone with
    correct deep-links. *(This plus 29–30 observed live on a phone = SUB-14 done.)*

## H. Admin & alerts (SUB-11) — DEV
32. **Dashboard truth** — counts by status match SQL; funnel tiles move when you gate/buy.
33. **Comp / re-grant** — admin grants a comp plan to a MAC → device allowed immediately.
34. **Fail-open alert** — force a verdict error (e.g. break DB creds briefly on dev):
    fail-open alert fires once, device still allowed (never bricked).
35. **Billing-issue spike** — POST ≥5 distinct-device `BILLING_ISSUE`s in one UTC day →
    one ops alert (once-per-day dedupe).

## I. Launch tooling (SUB-13) — DEV now, prod on launch day
36. **Seed dry-run** — done 2026-07-23 on dev: 21 bound / 1 existing / 20 to seed. ✔
37. **Seed apply (dev)** — `--apply` on dev: 20 rows created, "Coverage OK", re-run
    creates nothing new, the pre-existing row untouched.
38. **Comms dry-run** — done 2026-07-23: 1 notifiable parent. ✔ Apply on dev sends the
    announcement to that phone (that's yours — expect one push).
39. **Kill-switch drill** — item 9; runbook §1.

## J. Real-store gate (SUB-17) — ⏳ the launch blocker list
40. Products in both consoles (3 tiers, one Apple subscription group), RC entitlements
    mapped, prod keys in a release build (release refuses `test_` keys — verify the guard),
    RC webhook → prod URL + secret, then re-run items 12, 16, 17, 21, 22 on sandboxes.
41. Store review passes on both platforms (no external purchase links / price steering).

## K. Launch-day smoke — prod
42. Runbook §3–4: seed coverage gate → flip → one trial device talks, one lapsed test
    device gates → 24h watch (zero unexplained fail-opens) → day-30 validations calendared.

---

**Suggested order on DEV this week:** C9 (drill) → A → B → D11-15 → E18-20 → F → G → H →
I37-38. Everything ⏳ queues behind SUB-17.
