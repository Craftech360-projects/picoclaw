# Subscription implementation issues

Tracer-bullet slices from `docs/cheeko-subscription-spec.md` (2026-07-14). Each is a thin
end-to-end path, demoable on its own. `triage: afk-ready` = agent-runnable; `needs-human` = HITL.

> **2026-07-21 rails pivot**: purchases moved from the web portal to in-app Apple IAP +
> Google Play Billing via RevenueCat — `docs/superpowers/specs/2026-07-21-iap-subscription-rails-design.md`.
> SUB-6 (Razorpay, built) and SUB-8 (portal, never started) are closed; SUB-15/16/17 replace them.

```
closed:     SUB-1 (skeleton)  SUB-2 (trial gates)  SUB-3 (buckets)  SUB-4 (metering)
            SUB-5 (cutoff)    SUB-6 (Razorpay, shelved)  SUB-8 (portal, superseded)

unblocked:  SUB-15 (RC backend)   SUB-17 (store setup, HITL)   SUB-11 (admin)
            SUB-10 (parent app surfaces)   SUB-12 (Bulbul, HITL)   SUB-14 (push verify, HITL)

            SUB-15 + SUB-17 → SUB-16 (paywall) → SUB-9 (plan change)
            SUB-15 → SUB-7 (grace)
            SUB-3 + SUB-10 + SUB-15 + SUB-16 → SUB-13 (launch, HITL)
```

Claim an issue by setting `assignee:` in its frontmatter; close by setting `status: closed`
and appending a resolution note.
