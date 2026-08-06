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

- [ ] `/quiz-progress` has a bank selector offering quiz and riddle
- [ ] Device list, current level and levels-completed reflect the selected bank
- [ ] Set-level applies to the selected bank only; the other bank's level is unchanged
- [ ] Reset-day applies to the selected bank only
- [ ] Reset-day backdates rows rather than deleting them, and the affected level stays
      cleared afterwards
- [ ] Admin endpoints with no bank param still return quiz data (no client/server deploy
      ordering requirement)
- [ ] A device with progress in both banks shows both correctly

## Blocked by

- `docs/issues/riddle-bank/001-riddle-bank-over-http.md`
