---
status: closed
assignee: claude
---

# 003 — Importer speaks per-age vocabulary

## Parent

`docs/issues/per-age-banks/000-design.md`

## What to build

The bank importer currently validates `age_band` against `3-5`, `6-8`, `9+` and rejects
anything else. Change that vocabulary to the eight single ages, so authored per-age
content can be imported at all — this is what unblocks the content work in 006.

The importer's Excel date-guessing path (`normalizeAgeBand`) exists because a
spreadsheet reads `6-8` as a date. Single integers are not date-like, so that path simply
stops firing; leave it in place rather than deleting it — `-` bands may still appear in
an old sheet someone re-imports, and the clear rejection message is the point.

The ten-active-rows-per-level rule and the `|`-not-comma rule for `accepted_answers` are
unchanged; they now apply per age rather than per band, which follows for free from the
level-count key already including `age_band`.

## Acceptance criteria

- [x] A CSV with `age_band` values `3` through `10` imports cleanly into both banks
- [x] `age_band=6-8` is rejected with a message naming the eight valid values and the
      offending row number
- [x] Re-running the same import changes nothing — idempotent on `code`
- [x] The import exits non-zero when any (age, language, level) has other than 10 active
      rows
- [x] Existing importer unit tests pass with the new vocabulary; the duplicate-code and
      comma-in-accepted-answers cases still fail as before

## Blocked by

- `docs/issues/per-age-banks/001-per-age-content-exists.md` — the CHECK constraint must
  accept single-age values before any row carrying one can be written

## Resolution

`AGE_BANDS` is now the eight single ages, and the rejection message lists them so an
old sheet fails with its row number rather than importing content nobody will be
served.

**The vocabulary change exposed a real bug.** A bare age in a spreadsheet cell
arrives as a **number**, and every number went straight to the Excel date-serial
decoder — which reads `4` as 4 January 1900 and would have mangled the band on every
authored row. A value that is already a band now returns before any date handling.
Caught by writing the test first; it would not have shown up in a hand-typed CSV,
only in a real `.xlsx` from an author.

The date path itself is kept, with its purpose inverted. Excel still bakes a
hyphenated `6-8` into a date in one of two locale orientations, but there is nothing
left to recover it *into*, so both orientations now stay unrecognisable and are
rejected rather than silently reinterpreted as some unrelated age.

Round-tripped against the migrated database, both banks: 240 rows imported, 0
skipped, exit 0, and the 001 verifier still all-pass afterwards — the upsert matched
existing clone codes and created nothing. A one-row CSV carrying `age_band=6-8` was
rejected with `row 2: age_band must be one of 3, 4, 5, 6, 7, 8, 9, 10 (got "6-8")`
and exit 1.

Tests: 454 pass, 41 suites. Old three-band assertions were rewritten to their per-age
equivalents rather than dropped; `accepted_answers` pipe-separation, duplicate codes
and the ten-per-level rule are all still covered.

Beyond the stated scope, and worth knowing: **the seed CSVs were stale.** Every file
in `prisma/seed-data/` carried the retired vocabulary, so nothing in the repo could
be imported at all. The two `-all.csv` files are regenerated from the migrated
database with all eight ages, and the six band-split files are deleted — git history
keeps them. Issue 006 can start from `quiz-bank-all.csv` and upsert over the same
`-a<age>` codes.

