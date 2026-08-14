# 001 — Dump and read the `quiz_master` prompt

**Type:** HITL · **Status:** open

## Parent

[quizzy-redesign-gdd.md](../../design/quizzy-redesign-gdd.md) §12a, §14 action 1.

## What to build

No part of the Quizzy redesign is based on reading Quizzy's actual prompt. It is not
in this repo — it lives in the Manager DB, in `ai_agent_template`. Everything the GDD
says about current behaviour is inferred from ticket 006, ADR-0005 and the worker code
that consumes the prompt.

Dump the prompt, read it, and record what it actually says in a comment on this issue.
This is a read-only investigation. Nothing here changes the prompt — that happens in
008 and 010, each with its own backup.

```sql
SELECT agent_code, agent_name, length(system_prompt), length(greeting_prompt)
FROM ai_agent_template WHERE agent_code IN ('quiz_master','riddler');
```

**Trap:** the row is `agent_code = 'quiz_master'`, **not** `'quizzy'`. A query written
from the obvious guess matches zero rows and returns nothing silently — it already
produced an empty backup file once during ticket 006.

Four things to check in `system_prompt`, in priority order:

1. **What the two-tries-then-reveal flow actually says** — does it explain anything,
   or does it only state the answer? M2a (micro-teach) assumes the latter. If the
   prompt already teaches, M2a is smaller than specced, and issue 011 shrinks.
2. **Whether the day-gate / "refuse a second scored run" wording** survives the new
   mastery rule or now contradicts it.
3. **Which parts hardcode `revealed` semantics** in the MEMO instruction — these are
   the exact lines issue 008 must rewrite.
4. **Whether the multilingual judging rule** (added 2026-08-04) needs extending for
   the Doors. Door 3's guided phrasing is far looser than today's asks.

## Acceptance criteria

- [ ] Both rows located using `agent_code IN ('quiz_master','riddler')`; row count and prompt lengths recorded
- [ ] `system_prompt` for `quiz_master` dumped to a backup file, and the file confirmed non-empty before proceeding
- [ ] Each of the four checks above answered explicitly in a comment on this issue, quoting the relevant prompt lines
- [ ] Any GDD claim contradicted by the real prompt is listed, so the affected design doc can be corrected
- [ ] Stated explicitly whether M2a is still needed as specced (drives scope of 011)
- [ ] No `UPDATE` executed under this issue

## Blocked by

None - can start immediately. Needs Manager DB access (`D:\cheeko-backend\main\manager-api-node`).

---

## Findings — 2026-08-14

Read from the **local dev** Supabase project (`shlrfpbqkfnxqcmuatvs`:6543), the one
`manager-api-node/.env` points at. `system_prompt` 12,439 chars, `greeting_prompt`
2,428 chars, both backed up and non-empty. Read-only; no `UPDATE` run.

> **Verified on DB1 — 2026-08-14.** Dumped `quiz_master` from DB1
> (`tsiocygczplmnjpqmutc`) and diffed against the local copy: `system_prompt` and
> `greeting_prompt` are **byte-identical**, same 12,439 / 2,428 chars. `riddler` is
> absent on DB1 too, so that finding holds on both. Every finding below applies to DB1
> unchanged. **Prod is still a third copy and has not been read.**

**The `quiz_master` trap is real and now confirmed from both sides:** `agent_code` is
`quiz_master` while `agent_name` is `quizzy`. Anyone querying by the name they hear in
conversation gets zero rows.

**`riddler` does not exist on this database.** The query returned one row, not two.
Riddler's template is either only on DB1 / prod or is not an `ai_agent_template` row at
all. Issue 006 assumes a Riddler bank exists — confirm where before working it.

### 1. Does two-tries-then-reveal explain anything? — **No. M2a is needed as specced.**

§3, second unsuccessful attempt, in full: kindly reveal the answer, record MISSED and its
learning topic, immediately ask the next numbered question. It states the answer and
moves on. There is no explanation step anywhere in the loop. The GDD's assumption holds
and issue 011 does not shrink.

### 2. Day gate vs the new mastery rule — **one line contradicts it directly**

§4: *"The runtime guarantees the child has not already cleared the questions it gives
you, so you never need to check memory for repeats."*

After issue 008, a previously-revealed question **will** come back. That line tells the
model repeats never happen, and the child will notice the repeat before the model does.
It must be rewritten in 008, not left to be discovered.

The rest of the day gate is compatible: "exactly ten scored questions per local calendar
day" and "do not create a second scored Daily Ten on the same date" are orthogonal to
mastery, which operates across days.

Separately, §3's fixed ask → hint → reveal loop is a **two-attempt** structure. The Doors
model is three doors with one attempt each. §3 is a rewrite in issue 010, not a tweak.

### 3. `revealed` semantics in the MEMO — **the GDD's verdict mapping is wrong**

The MEMO contract emits **only two values**: `result=correct|revealed`. There is no
`wrong`. §6 spells it out — `correct` when the child got it right, `revealed` when you
told them the answer after their second try.

But §3 classifies answers into **four** buckets: FIRST_TRY, WITH_HINT, MISSED, UNCLEAR.
So **WITH_HINT collapses into `correct`** on the way to the database. Consequences:

- **Today's `correct` rows conflate unaided and hinted success.** The mastery bar — Door 1
  or 2 unaided clears — **cannot be reconstructed from existing data**. It is only
  enforceable once the attempt log (004) is recording, which is another argument for the
  004-before-008 ordering, independent of the one already in the GDD.
- **GDD §10 Step 1a's mapping is incorrect.** It proposes `solo → correct`,
  `helped → revealed`, `missed → wrong`. Live semantics are the opposite at both ends:
  `revealed` already means *missed*, and `wrong` is never emitted by Quizzy at all. The
  correct mapping is `solo → correct`, `helped → correct`, `missed → revealed`. Issue 005
  has been corrected.
- **Where do existing `wrong` rows come from?** `ANSWER_RESULTS` and the parent-app doc
  both list `wrong`, but this prompt cannot produce it. Issue 002 should count `wrong`
  rows by character while it is counting `revealed` — if Quizzy has any, something other
  than this prompt wrote them.

### 4. Multilingual judging and Door 3 — **the guard already tolerates paraphrase**

§4 already says the question may be phrased warmly in the model's own voice as long as
the thing being asked does not change, and §6 asks for `scored_text` as *"that same
question in a few plain words"* — so `questionTextMatchesBank` is **already** matching
against a paraphrase, not a verbatim copy. Door 3 widens an existing tolerance rather
than introducing a new one. Issue 011's re-verification still stands, but the risk is
smaller than the GDD assumed.

The multilingual rule (§3 Understand, §4) is answer-side only — it governs judging the
child's answer, not the phrasing of the ask. **Door 3 needs no multilingual extension.**

### 5. Not in the GDD: §5 hardcodes the age bands

§5 branches on **4-5 / 6-7 / 8-10** with per-band speaking instructions, and §2 reads the
age band from `USER.md`. Issue 013 collapses `age_band` to `'all'` in the data — but this
prompt would keep branching on bands the bank no longer has. **§5 is part of 013's
scope**, and the GDD did not account for it.

### Confirmed incidentally

- `greeting_prompt` carries `{{QUIZ_QUESTIONS}}` and `{{TODAY_DATE}}` — consistent with
  the session-start-only injection verified for issue 010.
- §2's "only today's `daily_quiz` MEMO in Saved State counts, never MEMORY.md or chat
  history" matches the known repeat-diagnosis behaviour and is load-bearing. Do not
  weaken it while editing §3.
