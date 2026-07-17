# Subscription implementation issues

14 tracer-bullet slices from `docs/cheeko-subscription-spec.md` (2026-07-14). Each is a thin end-to-end path, demoable on its own. `triage: afk-ready` = agent-runnable; `needs-human` = HITL.

```
unblocked:  SUB-1 (skeleton)   SUB-4 (metering)   SUB-12 (Bulbul, HITL)
            SUB-1 → SUB-2 (trial gates) → SUB-3 (buckets) → SUB-5 (cutoff, +SUB-4)
                          └→ SUB-6 (purchase) → SUB-7 (grace)
                                        ├→ SUB-8 (portal) → SUB-9 (plan change)
                                        └→ SUB-11 (admin)
            SUB-3 → SUB-10 (parent app)
            SUB-3 + SUB-6 + SUB-8 + SUB-10 → SUB-13 (launch, HITL)
            SUB-14 (phone push verification, HITL) — anytime; 80% criterion after SUB-3
```

Claim an issue by setting `assignee:` in its frontmatter; close by setting `status: closed` and appending a resolution note.
