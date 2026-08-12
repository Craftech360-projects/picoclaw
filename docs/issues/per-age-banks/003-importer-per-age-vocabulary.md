---
status: open
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

- [ ] A CSV with `age_band` values `3` through `10` imports cleanly into both banks
- [ ] `age_band=6-8` is rejected with a message naming the eight valid values and the
      offending row number
- [ ] Re-running the same import changes nothing — idempotent on `code`
- [ ] The import exits non-zero when any (age, language, level) has other than 10 active
      rows
- [ ] Existing importer unit tests pass with the new vocabulary; the duplicate-code and
      comma-in-accepted-answers cases still fail as before

## Blocked by

- `docs/issues/per-age-banks/001-per-age-content-exists.md` — the CHECK constraint must
  accept single-age values before any row carrying one can be written
