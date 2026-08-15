---
status: closed
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

- [x] The device table shows single-age bands (`4`, `7`) for both the quiz and riddle
      selectors, with no `3-5`-era labels left in the UI
- [x] Any band filter or legend offers all eight ages — n/a, none exists
- [x] `set-level` on a device in bank `7` moves it to the requested level and the row
      reflects it on reload, for both banks
- [x] `reset-day` re-opens the Daily Ten for a device in a per-age bank without
      dropping its level
- [x] A device whose kid has no birth date displays the defaulted band distinctly, as
      it did before

## Blocked by

- `docs/issues/per-age-banks/002-children-derive-per-age-bank.md` — until devices derive
  into per-age banks there is nothing per-age to display

## Resolution

**No code changed.** `manager-web/src/views/QuizProgress.vue` was already fully
data-driven: the Band column renders `{{ row.age_band }}` with a `default` tag beside
`age_band_defaulted`, the set-level picker bounds itself with `:max="target.max_level"`,
and the only selector on the page chooses the bank (quiz/riddle), which is unrelated
to age. There is no band filter and no legend enumerating bands, so nothing enumerated
three of anything. This is the outcome the ticket anticipated; inventing work here
would have been the mistake.

Verified by exercising the data the page renders, through the same service functions
the admin endpoints call:

- `allDeviceProgress` for both banks returns 11 devices with bands `[4, 6, 8, 9]` —
  single ages, no retired labels — and 6 devices correctly flagged
  `age_band_defaulted` (no kid profile).
- `setLevel(mac, 3)` on a band-`4` device: 20 backdated cleared rows, then
  `current_level = 3`, `levels_completed = 2`. Both banks.
- `clearDayGate` after ten answers dated today: `answered_today` 10 → 0,
  `day_complete` true → false, `backdated = 10`, and `current_level` stayed 2 with one
  level still cleared. The level survives, which is the whole reason reset-day
  backdates rather than deletes.

The first reset-day attempt reported `backdated = 0` and proved nothing — `setLevel`
already dates its rows yesterday, so there was nothing from today to move. Re-run with
rows actually dated today, as above.

Test device `3C:0F:02:D4:89:54` was chosen because it had zero answer history; the run
refused to proceed otherwise, and deleted exactly the 50 rows it created, restoring it.

Seen in the browser at `localhost:8001/#/quiz-progress` after the fact (2026-08-12, the
manager-web and manager-api both running locally). Both the Quiz and Riddles tabs render
`Band 8` for `00:16:3E:AC:B5:38` with no retired label anywhere, level `2 / 3` and
`3 / 3` respectively, and `Today 0 / 10`. Nothing about the layout suffers from the
shorter value.

The browser pass did find something the payload checks had not: the `Correct` column
read **22** for a device with 11 real quiz answers, and **52** for 31 riddle answers —
the 001 remap copied rows rather than moving them, and the lifetime tallies are not
band-scoped. Not an admin-page defect, so it is not fixed here; written up with its
remedy in [005](005-retire-old-bands.md), which is the step that makes deleting the
copies safe.
