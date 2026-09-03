# 11. The Device Pairing Identifies the Child, Not Their Voice

Date: 2026-09-03

## Status

Accepted

Rests on [ADR-0007](0007-device-owned-turn-boundary-batch-stt-for-manual-talk.md): the tap
is the turn boundary, so a turn has one speaker and there is no continuous multi-speaker
stream to segment. Evidence in
[docs/child-vs-parent-speaker-research.md](../child-vs-parent-speaker-research.md).

## Context

A feature was proposed: have Cheeko detect when the child is speaking versus the parent,
and use it for conversation understanding, parental context, safety and analytics. The
obvious implementation is speaker diarization, with speaker enrollment as the "proper"
version.

Investigating it turned up three things that change the shape of the question.

**Diarization does not answer it.** Diarization produces anonymous clusters — `spk_1`,
`spk_2` — and cannot say which cluster is the child. Answering "kid or parent" requires
either an enrolled voiceprint per person or an age-group classifier. These are different
problems with different costs, and the feature request conflates all three.

**Both vendors' diarization is batch-only, at conversational-fatal latency.**
`gemini-3.5-transcribe-live` explicitly does not support it. The batch
`gemini-3.5-transcribe` does — it returns word-level `{"speaker":"spk_1"}` annotations —
but measured **5.0s median over 12 runs** (4.7–5.6s), flat regardless of clip length,
against ~540ms for the Live model and 533ms for `sarvam_rest`. Sarvam's diarized tier
(₹45/hour against ₹30) is likewise batch, with latency documented in minutes. Nothing
that answers the question runs inside a turn.

**Voiceprints are the worst option on both axes at once.** Technically, they decay as
children grow: 22.5% EER same-year, **34.7% at a three-year gap**, and 9.6–13.2% EER even
same-year on five-year-olds. Enrollment is therefore not one capture but a recurring
product task. Legally, voiceprints are named biometric identifiers in the amended COPPA
Rule, and collecting one **destroys the § 312.5(c)(9) exception** that currently permits a
voice toy to take child audio at all — trading a working legal basis for a feature.

And there is a prior nobody was counting. Cheeko is push-to-talk and the device is paired
to one child; that binding already carries memory, quiz progress and the workspace. The
product does not invite a parent to tap the toy and talk — parents act through the app.
The answer is already in hand, for free.

## Decision

**Cheeko does not identify speakers by voice. The device→child pairing is the identity of
record, and the tap is what makes a turn attributable.**

Concretely:

1. **No voiceprint enrollment, ever, for a child.** Not for identification, not for
   verification, not as an "optional" parent-enabled feature. This is the line the COPPA
   § 312.5(c)(9) exception sits behind.
2. **No speaker diarization in the live path.** It cannot answer the question, and the
   only implementations that offer it cost ~10x the entire STT budget.
3. **A turn belongs to the paired child** unless something outside the audio says
   otherwise. `stt.Provider.Capabilities().SupportsDiarization` stays `false` for the
   providers we ship, and no consumer may come to depend on it.
4. **No per-child voice attributes in analytics.** Not pitch, not age estimates, not
   speaker embeddings — nothing derived from the voice that persists as a property of the
   child.

## What this does NOT decide

**Age-group classification is not banned outright** — it is not adopted, and it is not to
be adopted on convenience grounds. A stateless classifier that answers "this turn sounds
like an adult" without storing anything is a materially different proposition from a
voiceprint, and could be revisited if a concrete need appears that the pairing genuinely
cannot serve. It would still have to clear §9(3) (below) and would still be measured
against a prior it is unlikely to beat: adult-female and older-child F0 ranges overlap, so
the classifier is weakest precisely where the product would lean on it.

**Batch diarization for offline work is untouched.** Nothing here forbids using the batch
model with speaker annotations on already-consented recordings for debugging or evaluation,
off the live path. The prohibition is on voice-derived *identity*, not on the annotation
existing.

**This does not settle the legal question.** See below — it avoids it.

## Consequences

- **The feature's stated goals get a cheaper answer or none.** Conversation understanding
  and parental context are served by the pairing. Safety framed as "was that an adult
  speaking" is not served, and we accept that. If safety needs it, that requirement should
  be stated on its own and argued on its own, not carried in on the back of analytics.
- **A parent who leans in and speaks during the child's turn is recorded as the child.**
  Accepted. The turn is tap-scoped and short; the mislabel is bounded to that turn and
  carries no persistent consequence.
- **We stay inside the COPPA exception** that lets a voice toy take child audio without
  full verifiable parental consent per capture, and we avoid holding biometric identifiers
  of children.
- **We avoid an argument we cannot currently win.** DPDP §9(3) prohibits "tracking or
  behavioural monitoring of children" — not consent-waivable, ₹200 crore exposure — and
  the Act defines neither term. Each phrase appears exactly once, inside §9(3) itself;
  "profiling" never appears. Rule-level clarification was not obtainable. A persistent
  per-child voice identity feeding parent-facing analytics is the kind of thing that
  argument would be about. Not building it means not having the argument.
- **We do not repeat the Alexa fact pattern.** In *US v. Amazon.com (Alexa)* (2023) the
  FTC's account records Amazon's justification that "Children's speech patterns and accents
  differ from those of adults, so the unlawfully retained voice recordings provided Amazon
  with a valuable database for training the Alexa algorithm" — $25M penalty, mandated
  deletion, and an order barring use of that data for training
  ([press release](https://www.ftc.gov/news-events/news/press-releases/2023/05/ftc-doj-charge-amazon-violating-childrens-privacy-law-keeping-kids-alexa-voice-recordings-forever),
  [case page](https://www.ftc.gov/legal-library/browse/cases-proceedings/192-3128-amazoncom-alexa-us-v)).
  ADR-0007 already records that child speech breaks ordinary ASR assumptions, so that
  premise is true here. The order is the precedent that its being true does not make the
  retention lawful. This decision removes the internal incentive to make that argument.
- **There is no clean commercial training path anyway.** The public child-speech corpora
  that would support an in-house classifier are uniformly non-commercial — MyST and
  CHILDES/TalkBank are CC BY-NC-SA, with TalkBank's terms explicitly precluding commercial
  incorporation; CSLU Kids and CMU Kids are LDC-gated. Building our own would be a
  child-audio collection programme, which is the §9(3) and COPPA problem again.
- **Reversal is cheap for classification and expensive for enrollment.** A stateless
  classifier could be added later without unwinding anything. Enrollment could not: it
  would require consent flows per child, deletion machinery reaching derived artifacts and
  trained models, a defensible retention policy for data whose value is that it is not
  deleted, and re-enrollment at least annually. Deciding against it now costs nothing;
  deciding for it later would cost all of that regardless of when it was decided.

If this is revisited, the note's §9 lists ten open questions and how to close each — start
there rather than re-deriving.
