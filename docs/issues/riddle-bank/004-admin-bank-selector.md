---
status: closed
assignee: claude
---

# 004 — Admin bank selector

## Parent

`docs/issues/riddle-bank/000-design.md`

## What to build

The Quiz Progress admin page currently shows one bank because one bank existed. Add a bank
selector so a child's riddle progress is visible and adjustable the same way their quiz
progress is.

Extend the existing page at `/quiz-progress` in `manager-web` rather than duplicating it —
the two banks have identical shape, so a selector is the whole change on the client.

The three admin endpoints (`GET /quiz/admin/devices`, `POST /quiz/admin/set-level`,
`POST /quiz/admin/reset-day`) each take the bank parameter added in 001 and default to the
quiz bank, so the page works unchanged until the selector sends one.

One trap to respect, carried from the quiz bank: reset-day must **backdate** answer rows,
never delete them. Deleting resets the day gate but also un-clears levels, silently
destroying progress. Whatever the quiz path does here, the riddle path must do the same.

## Acceptance criteria

- [x] `/quiz-progress` has a bank selector offering quiz and riddle
- [x] Device list, current level and levels-completed reflect the selected bank
- [x] Set-level applies to the selected bank only; the other bank's level is unchanged
- [x] Reset-day applies to the selected bank only
- [x] Reset-day backdates rows rather than deleting them, and the affected level stays
      cleared afterwards
- [x] Admin endpoints with no bank param still return quiz data (no client/server deploy
      ordering requirement)
- [x] A device with progress in both banks shows both correctly

## Blocked by

- `docs/issues/riddle-bank/001-riddle-bank-over-http.md`

## Resolution

Shipped as `e59ec844` in `manager-web` on `feat/riddle-bank`. All seven criteria pass.
**Client-only** — the ticket was right that 001 had already done the server half, so no
API file was touched.

**No deploy ordering either way**, which is the criterion worth restating: the endpoints
default to quiz when `bank` is absent, so an older page against a newer API still gets
quiz data, and this page against an API that ignores the param also still works.

**Two hazards the ticket did not name, both handled:**

1. `set-level` captures the bank when the **dialog opens**, not when Submit is pressed.
   It rewrites an answer log, and `target` is captured at open time — reading the bank at
   submit time would let the selector move underneath a pending dialog and rewrite the
   wrong bank. Found reviewing the diff. The modal overlay makes it hard to trigger
   today, but the one destructive operation on the page should not depend on an overlay
   for correctness.
2. `fetchData` captures the requested bank and discards a response whose bank no longer
   matches, so a slow quiz reply cannot paint itself into the riddle table.

**Verified against the dev database**, one device with real progress in both banks:

- no `bank` param and `bank=quiz` return byte-identical rows; `bank=riddle` returns
  genuinely different state for the same device — level 2 vs 1, 2 levels cleared vs 0,
  21 correct vs 3
- `set-level` to 3 on the riddle bank moved the riddle row and left the quiz row
  byte-identical
- `reset-day` on riddle backdated 3 rows: riddle row count stayed 30, quiz row count
  stayed 3, `answered_today` fell to 0 and `levels_completed` **stayed at 2** — the
  ticket's stated trap, held
- device restored to level 2 afterwards

`vue-cli-service build` clean before and after the review fixes. There is no unit-test
seam in `manager-web` (no test runner is configured in its `package.json`), so the
verification above is the evidence; the API behaviour it exercises is covered by the
Node suites from 001.
