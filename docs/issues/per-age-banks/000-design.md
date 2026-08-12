# Per-age question banks — one bank per age, 3 through 10

Design, 2026-08-12. Spans `D:\cheeko-backend\main\manager-api-node` (Node/Prisma),
`manager-web` (admin), and touches `D:\picoclaw` only cosmetically.

Today a child plays against one of three Age Bands (`3-5`, `6-8`, `9+`). This replaces
the three bands with **eight per-age banks**: every age from 3 to 10 gets its own
authored content. A child plays exactly the bank matching their age this year and moves
to the next bank on their birthday.

Background reading, not repeated here:
`docs/adr/0005-quizzy-scored-questions-come-from-a-curated-bank.md`,
`docs/issues/riddle-bank/000-design.md`, `CONTEXT.md`.

---

## 1. The load-bearing observation

`age_band` is an opaque `VARCHAR(10)` everywhere except three places:

| Place | What it knows |
|---|---|
| `quiz.logic.js` `ageBandFromBirthDate` | maps birth date → one of three strings |
| The two migrations' CHECK constraints | `IN ('3-5','6-8','9+')` |
| `scripts/lib/quiz-import.js` `AGE_BANDS` | the same vocabulary for CSV validation |

`deriveLevelState`, the day gate, champion replay, `banks.js`, every endpoint, the
admin selectors, and the **entire Go worker** treat the value as a string key. The
worker renders it inside the prompt block ("Level 2, band 6-8" becomes "Level 2,
band 6") and echoes it nowhere else. **No picoclaw deploy is required.**

So the redesign is: same column, new domain — `'3'..'10'` — plus content and a
data migration. Nothing structural.

---

## 2. Decisions

| Decision | Choice | Why |
|---|---|---|
| Band values | Strings `'3'`–`'10'` in the existing column | No schema shape change, index unchanged, worker unchanged |
| Under 3 | Clamp to `'3'` | Youngest authored content; product has no under-3 mode |
| 10 and over | Clamp to `'10'` | A 12-year-old plays the 10 bank; `'10+'` buys nothing over clamping |
| Missing birth date | Default `'6'` (was `'6-8'`), still flagged `age_band_defaulted` | Same midpoint default, new vocabulary |
| Name | Keep the `age_band` column and `ageBandFromBirthDate` name | Renaming ripples through schema, service, tests, worker JSON for zero behaviour. CONTEXT.md redefines **Age Band** as a one-year cohort |
| Seed content | Clone each band's rows into each constituent age (`3-5` → 3, 4, 5; `6-8` → 6, 7, 8; `9+` → 9, 10), deactivate the originals | Ships 8 full banks on day one with zero authoring; differentiation is follow-up authoring, not a blocker |
| Clone codes | `<old-code>-a<age>` (e.g. `q35-l1-01-a4`) | `code` is unique; suffix keeps the parent traceable. Verify longest existing code + 3–4 chars ≤ 50 |
| Child progress | Remap answer rows onto the clones in the child's **current** age bank | Without it every child restarts at level 1 and re-hears cleared questions. ~20 lines of SQL, runs once |
| Old band rows | `active=false`, never deleted | FK is RESTRICT; the answer log is append-only history |
| Both banks | Quiz and riddle migrate together | Same schema, same importer, same admin page; half-migrating leaves the admin UI with two vocabularies |

---

## 3. Data migration (one SQL file, applied to each bank's table pair)

**Expand → migrate → contract, in three separately deployable steps.** Retiring the old
rows in the same transaction as the clone would be a live outage: `loadBank` filters
`active: true`, so between that SQL and the code deploy every child queries `'6-8'`,
finds nothing, and gets the free-chat fallback. The old rows must stay active until the
mapping stops asking for them.

**Step 1 — expand (issue 001).** Additive; nothing reads the new rows yet.

```sql
-- Widen the vocabulary to accept BOTH, so old and new rows coexist
ALTER TABLE quiz_question DROP CONSTRAINT quiz_question_age_band_check;
ALTER TABLE quiz_question ADD CONSTRAINT quiz_question_age_band_check
  CHECK (age_band IN ('3-5','6-8','9+','3','4','5','6','7','8','9','10'));

-- Clone every active band row into each constituent age
INSERT INTO quiz_question
  (code, question_text, answer_text, accepted_answers, category, age_band, level, language, active)
SELECT q.code || '-a' || a.age, q.question_text, q.answer_text, q.accepted_answers,
       q.category, a.age::text, q.level, q.language, q.active
FROM quiz_question q
JOIN LATERAL (
  SELECT unnest(CASE q.age_band
    WHEN '3-5' THEN ARRAY[3,4,5]
    WHEN '6-8' THEN ARRAY[6,7,8]
    WHEN '9+'  THEN ARRAY[9,10]
  END) AS age
) a ON true
WHERE q.age_band IN ('3-5','6-8','9+')
ON CONFLICT (code) DO NOTHING;   -- re-runnable
```

**Step 2 — migrate (issue 002).** The code deploy flips `ageBandFromBirthDate`. Children
now read the per-age banks; the old rows are still active but nothing queries `'6-8'`
any more. Rollback is reverting the deploy — the data supports both vocabularies.

**Step 3 — contract (issue 005), after a soak.** History stays; the FK is RESTRICT and
nothing is ever deleted.

```sql
UPDATE quiz_question SET active = false WHERE age_band IN ('3-5','6-8','9+');
ALTER TABLE quiz_question DROP CONSTRAINT quiz_question_age_band_check;
ALTER TABLE quiz_question ADD CONSTRAINT quiz_question_age_band_check
  CHECK (age_band IN ('3','4','5','6','7','8','9','10'));
```

Each step repeats verbatim for `riddle_question` (constraint names differ).

### Progress remap (per device, both answer tables)

Runs in step 1, after the clone. For each answer row against an old-band question,
insert the equivalent row against the clone **in the device's current age bank**,
preserving `answered_at` and `result`. The `NOT EXISTS` guard makes it re-runnable, so
it can be replayed immediately before the step 2 deploy to pick up anything answered in
between:

```sql
INSERT INTO quiz_question_answer (device_mac, question_id, result, answered_at)
SELECT a.device_mac, clone.id, a.result, a.answered_at
FROM quiz_question_answer a
JOIN quiz_question old ON old.id = a.question_id AND old.age_band IN ('3-5','6-8','9+')
JOIN device_age d ON lower(d.mac) = lower(a.device_mac)   -- see note
JOIN quiz_question clone
  ON clone.code = old.code || '-a' || d.age
WHERE NOT EXISTS (
  SELECT 1 FROM quiz_question_answer dup
  WHERE dup.device_mac = a.device_mac AND dup.question_id = clone.id
    AND dup.answered_at = a.answered_at
);
```

`device_age` is a CTE computing each device's clamped age from
`ai_device.kid_id → kid_profile.birth_date` — same arithmetic as
`ageBandFromBirthDate`, in SQL. Devices with no kid/birth date map to 6.

Two known costs, both acceptable:

- **answered_today double-count for one day.** `answered_today` counts rows since
  midnight device-wide; remapped copies of *today's* rows count alongside the
  originals. Run the migration late evening IST, or accept one day where the day
  gate closes early.
- **Cross-band progress goes dormant, not remapped.** A child who is 7 keeps only
  the rows remapped into bank `'7'`. Their history in the other ages' clones stays
  empty — correct: they never played that content *as that age*.

---

## 4. Code touch points

### manager-api-node

| File | Change |
|---|---|
| `src/services/quiz.logic.js` | `ageBandFromBirthDate` returns `String(min(10, max(3, age)))`; age computation itself unchanged. JSDoc: returns `'3'..'10'` |
| `src/services/quiz.service.js` | `DEFAULT_AGE_BAND = '6'`. Nothing else — band is opaque below this line |
| `scripts/lib/quiz-import.js` | `AGE_BANDS = new Set(['3','4','5','6','7','8','9','10'])`; error message updated. `normalizeAgeBand` keeps working (single integers are not date-like, so the Excel date-guess path simply never fires — leave it) |
| `prisma/schema.prisma` | Comment-only if the check lives in SQL; regenerate client anyway |
| `prisma/seed-data/*.csv` | New per-age CSVs as authoring replaces clones (follow-up, §6) |
| `src/services/mobile.service.js` | Verify it treats `age_band` as a label (expected); parent app shows "6" instead of "6-8" |
| Tests | `quiz.logic` band-mapping table (3→'3', 5→'5', 6→'6', 9→'9', 11→'10', 2→'3', null→null); importer vocabulary; existing service tests re-green with new fixtures |

### manager-web

- `/quiz-progress` band display and any band filter: 3 options → 8. If it renders
  whatever the API returns, verify only.

### picoclaw (no deploy required)

- Zero code change. `QuizBatch.Band` is opaque; the prompt block reads
  "band 6" — grammatically fine.
- Doc-only: CONTEXT.md **Age Band** entry redefined ("one-year cohort, ages 3–10,
  clamped at both ends"); **Level** entry unchanged.

---

## 5. Slices

Published as `docs/issues/per-age-banks/001`–`007`.

| # | Slice | Type | Blocked by |
|---|---|---|---|
| [001](001-per-age-content-exists.md) | Per-age content exists in both banks (expand + remap) | AFK | — |
| [002](002-children-derive-per-age-bank.md) | Children derive into their per-age bank | AFK | 001 |
| [003](003-importer-per-age-vocabulary.md) | Importer speaks per-age vocabulary | AFK | 001 |
| [004](004-admin-eight-banks.md) | Admin shows eight banks | AFK | 002 |
| [005](005-retire-old-bands.md) | Retire the old bands (contract) | AFK | 002 + soak |
| [006](006-differentiate-ages-3-4-5.md) | Differentiate ages 3, 4 and 5 | HITL | 003 |
| [007](007-production-promotion.md) | Production promotion | HITL | 005 |

002 must not deploy before 001 has run, or every fetch queries a band value (`'4'`) that
has no rows and every child gets the free-chat fallback. Same order on prod.

---

## 6. Content obligation (the real cost)

Cloning means age 3 and age 5 start with **identical** content — the banks exist but
are not yet differentiated. Full differentiation is 8 ages × 3 levels × 10 × 2 banks
= **480 authored rows** (was 180). Authoring priority, per production device data
(nine of ten profiled devices are in today's 3-5 band):

1. Ages 3, 4, 5 — differentiate first, they serve almost all real users.
2. Ages 6, 7, 8.
3. Ages 9, 10 — lowest population; clones can live longest here.

The frontier warning (`FRONTIER_WARN_LEVELS = 3`) fires per band; with thinner
per-age pools it will fire more. That is signal, not noise — leave it.

Per-age CSVs replace clones by upserting the same `-a<age>` codes, then flipping
authored rows in via `active` — no id churn, no progress loss.

---

## 7. New behaviour worth stating out loud

- **Birthday transitions happen every year now**, not at 6 and 9. On the first
  session after a birthday the child derives into the next bank at its level 1;
  their old bank's progress stays in the log, dormant. This is the design working,
  not a bug — but parents may ask why the child "restarted".
- The day gate is device-wide (`answered_today` is not band-scoped), so a birthday
  mid-day cannot double the Daily Ten.
- `kid_learning_progress` milestone topics become `"<age> level <n>"`. Old
  `"3-5 level 1"` rows survive untouched; the (kid, subject, topic) key cannot
  collide with the new vocabulary.

---

## 8. Explicitly out of scope

- Any picoclaw code change or deploy.
- Per-age prompt/personality changes — the characters' prompts don't mention bands.
- A finer value metric than one year (half-years, months).
- Retiring the clone rows once authored content replaces them (they retire via
  `active=false` naturally).
- The open quiz-bank items (007 API-down path, 008 prod promotion) — unchanged by
  this design, but slice 6 should ride the same prod window as 008 if it is still
  open.

## 9. Known traps that apply here

Carried from the quiz/riddle builds; all still live:

- Production never used `prisma migrate` — `_prisma_migrations` does not exist.
  Apply the SQL directly; `migrate deploy` would replay everything.
- Run `prisma generate` after any schema edit or the client silently serves stale
  models.
- `prisma.config.ts` overrides `DATABASE_URL` on the dev box — confirm the printed
  datasource host before concluding anything from a query.
- Plain `node` scripts do not load `.env`; `set -a && . ./.env && set +a` first.
- `code` is `VARCHAR(50)`: check `SELECT max(length(code)) FROM quiz_question`
  before cloning; the `-a10` suffix adds 4 chars.
- Test level advancement by backdating answer rows, never deleting them
  (`quizzy-level-advance-testing`).
