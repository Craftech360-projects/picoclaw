# 002 — Selection endpoint: GET /quiz/next-questions

**Type:** AFK · **Status:** closed
**Spec / Plan:** as 001 (plan Tasks 2–3, selection half)
**Repo:** manager-api-node

## What to build

The selection path end to end: pure helpers (age → Age Band mapping; Current Level derivation = lowest level with uncleared questions) driven by Jest tests, and `GET /quiz/next-questions?device_mac=...` returning the current Level's uncleared questions for the device's band and language.

Behavior (all from the spec — it is the contract):

- Band derived server-side from the device's kid profile birth date; missing → default `6-8` with `age_band_defaulted: true`.
- Language from the kid profile, fallback to `en` when the band has no content in that language.
- Nothing is marked on fetch; the answer log is the only write (written by issue 003's endpoint).
- **Champion replay:** all levels cleared → return the least-recently-played level's full ten with `replay: true`; partial replay days do not resume.
- Log a warning when the device is within 3 levels of the authored frontier.
- Auth: same service-key middleware as the existing character-session endpoint.

Response: `{age_band, age_band_defaulted, language, level, replay, frontier_warning, questions: [{id, question_text, answer_text, accepted_answers}]}` — ids as strings.

## Acceptance criteria

- [ ] Jest tests for band mapping (boundary birthdays) and level derivation (partial level, question added to cleared level, all-cleared, empty bank) pass
- [ ] `curl` for test device `00:16:3e:ac:b5:38` returns the seeded level-1 batch
- [ ] Empty bank for a band → `200` with `questions: [], level: null` (not an error)
- [ ] Replay and frontier warning verified with hand-inserted answer rows
- [ ] Committed on the manager repo branch

## Blocked by

- 001 (tables + seed content) — closed

## Resolution

Shipped in `5520e416` (manager-api-node). 18 pure-logic Jest tests green; full suite 367
passing with only the known pre-existing `prisma-client-guard` failure. Verified by curl
against dev DB2: 10 questions at level 1 with string ids; unknown MAC returns band `6-8`
with `age_band_defaulted: true`; empty bank returns 200 with `level: null`; champion
replay confirmed by clearing all 20 and backdating level-2 rows (replay follows
`max(answered_at)`, not level order); a partially-cleared level returns only its
remaining 7; missing `device_mac` returns 400; no service key returns 401.

**Two integration facts the worker depends on** (both verified compatible with SUB-004):
- The API mounts under base path `/toy`, so the real URL is `/toy/quiz/next-questions`.
  The Go client appends to a base that already carries `/toy`, same as the working
  character-session call.
- Device MACs are stored upper-case; lookup normalises, and answer-row reads are
  case-insensitive so clears match whatever case SUB-003 writes.

`language` in the response reports the *effective* language questions came from (falls
back to `en`), not the raw profile value. Not consumed by the worker.
