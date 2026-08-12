---
status: open
assignee: claude
---

# 004 — Admin shows eight banks

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

The admin quiz-progress page in `manager-web` shows each device's age band, current
level and today's count, with a bank selector and the set-level / reset-day escape
hatches. Everything that enumerated three bands must now enumerate eight.

Start by checking what actually hard-codes the vocabulary. If the page renders whatever
`age_band` the API returns, this slice is verification only — say so and close it
rather than inventing work. The likely real change is a band filter or a legend.

`set-level` and `reset-day` are per bank and per device; neither takes an age band as
input, so they should need no change beyond displaying the new value.

## Acceptance criteria

- [ ] The device table shows single-age bands (`4`, `7`) for both the quiz and riddle
      selectors, with no `3-5`-era labels left in the UI
- [ ] Any band filter or legend offers all eight ages
- [ ] `set-level` on a device in bank `7` moves it to the requested level and the row
      reflects it on reload, for both banks
- [ ] `reset-day` re-opens the Daily Ten for a device in a per-age bank without
      dropping its level
- [ ] A device whose kid has no birth date displays the defaulted band distinctly, as
      it did before

## Blocked by

- `docs/issues/per-age-banks/002-children-derive-per-age-bank.md` — until devices derive
  into per-age banks there is nothing per-age to display
