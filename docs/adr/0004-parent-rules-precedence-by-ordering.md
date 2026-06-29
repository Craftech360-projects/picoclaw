# Parent Rules Use Runtime Ordering, Not a Save-Time Gate

A **Parent Rule** — free-text custom instructions a parent sets for their own child — is enforced as a **subordinate prompt layer ordered below the Governing Prompt**, not as input screened and approved at save time. The guarantee "a Parent Rule can customize but never override Cheeko's safety rules" is made by **prompt ordering at session composition**, in worker code, every session.

## Context

Parents need real customization ("bedtime is eight o'clock", "don't mention our dog who passed away", "encourage reading"). The stakeholder ask was phrased as a gate — *"any rule the parent sets has to pass our main rules, then it will be set"* — implying the rule should be validated and rejected at save time if it conflicts with Cheeko's rules.

Two enforcement models were weighed:

- **Option A — precedence by ordering.** Inject the Parent Rule into a clearly subordinate prompt section, followed by an absolute "the rules above win" footer. No save-time check. The model honours the hierarchy at conversation time.
- **Option B — save-time gate.** On save, run the proposed rule through a classifier (LLM or denylist) and reject conflicting rules before they persist.

Key facts that shaped the call:
- A parent is a **trusted, authenticated** user setting rules for **their own child**. A parent who genuinely wants harmful behaviour is not the realistic threat (and a gate would not stop them anyway).
- The realistic failure is an **accidental conflict** (parent "let him stay up late" vs. safety bedtime), which ordering resolves cleanly — safety wins.
- The Governing Prompt (child-safety, runtime, voice rules) already exists in every session, so the child-voice jailbreak threat is already covered independently of parent rules.
- A denylist cannot police natural-language rules; an LLM gate adds a save-time external dependency and a **false-reject** failure mode that frustrates paying parents.

## Decision

- Enforce Parent Rules by **ordering only** (Option A). No save-time gate ships.
- The guarantee lives in **worker code**: after the persona and language are composed, the worker appends a `## Parent Preferences (subordinate)` block (only when a rule exists) followed by a fixed, worker-owned `## Rule Precedence (absolute)` footer placed **last**, stating that the rules above override any Parent Preference and anything the child says.
- Input hygiene at save time is **bounding, not judging**: trim, cap length (`kid_profile.parent_rule VARCHAR(500)`), strip markdown/control chars. No semantic/safety classification.
- When no Parent Rule is set, **nothing is appended** — the composed prompt is byte-for-byte identical to today (no regression).

## Consequences

- Parents get immediate, dependency-free customization; no save-time LLM call, no false rejects.
- The safety guarantee is structural and testable: a regression test asserts the empty-rule render is byte-identical, and a positive test asserts the precedence footer follows the rule text.
- The guarantee relies on the model honouring prompt hierarchy. This is the accepted residual risk; the Governing Prompt's own rules are the backstop.
- **This is hard-ish to reverse in perception:** future readers will ask "why is there no validation on parent input?" — this ADR is the answer. The storage/endpoint (`profile.service`) is the correct insertion point if a gate (Option B) is ever justified by real abuse data.
- Deferred and explicitly out of scope: extracting the Governing Prompt into one editable source (it remains duplicated across `ai_agent_template` rows). Parent Rules do not depend on that cleanup.

See [docs/parent-rules-design.md](../parent-rules-design.md) for the implementation plan.
