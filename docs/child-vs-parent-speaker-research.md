# Telling a child speaker from a parent speaker

Research note. 2026-09-03.

**Where this lives and why.** `docs/` keeps flat topic notes (`cheeko-pricing-strategy.md`,
`chat-history-prod-rollout.md`, `config-versioning.md`) with `adr/` reserved for decisions.
There is no `docs/research/`. This is a topic note, not a decision, so it sits flat in
`docs/`. If the team acts on it, the decision belongs in a new ADR that cites this file.

Every factual claim below carries the URL of the source that owns it. Claims I could not
verify against a primary source are marked **UNVERIFIED** and collected in §9.

---

## 1. Verdict

**Feasible, but the feature as requested is the wrong shape, and the legal exposure of the
"proper" version is the deciding factor.**

Three things in one sentence each:

1. **You almost certainly already know the answer.** Cheeko is push-to-talk and the device
   is paired to one child. The tap is the turn boundary (ADR 0007) and the device→kid
   binding is already load-bearing for memory, quiz progress and the workspace
   (`docs/mobile-active-child-selection.md`, `docs/child-owned-state-prod-rollout.md`).
   The prior "this turn is the paired child" is very strong, and nothing in the product
   invites a parent to tap the toy and talk — parents act through the app
   (`docs/parent-rules-design.md`). Buying a classifier to re-derive a fact you already
   hold is the expensive way to get a slightly worse answer.
2. **Diarization does not answer the question.** Both vendors' diarization is batch-only,
   returns anonymous cluster labels (`spk_1`), and exists to segment continuous
   multi-speaker audio. A PTT turn is a single-speaker clip. See §3 and §4.
3. **The version that would actually answer it — an enrolled child voiceprint — is the
   single biggest obstacle, and it is legal, not technical.** A stored voiceprint is
   expressly a biometric identifier under the amended COPPA Rule
   ([16 CFR 312.2](https://www.ecfr.gov/current/title-16/chapter-I/subchapter-C/part-312)),
   and collecting it destroys the one exception that currently lets a voice toy take a
   child's audio without per-recording parental consent
   ([§ 312.5(c)(9)](https://www.ecfr.gov/current/title-16/chapter-I/subchapter-C/part-312)).
   In India, DPDP §9(3) flatly prohibits "tracking or behavioural monitoring of children",
   with no legitimate-interest override and a penalty of up to ₹200 crore
   ([DPDP Act 2023, gazette text](https://www.meity.gov.in/static/uploads/2024/06/2bf1f0e9f04e6fb4f8fef35e82c42aa5.pdf)).
   See §8.

**Recommendation:** do not build enrollment. If you want a signal beyond the pairing prior,
the only proportionate option is a stateless, per-turn, child-vs-adult *classification* that
stores no voice-derived identifier — and even that should be justified against DPDP §9(3)
before it ships. Cheapest honest version: ask the model you are already sending the audio to,
off the critical path, and store one boolean. Set expectations accordingly: the best
published accuracy for binary child-vs-adult on *spontaneous* audio is **73–77 macro F1**
(§5.4), not the 95%+ that read-speech benchmarks advertise, and it degrades further on the
under-6 band that Cheeko is built for.

---

## 2. Context this answer is shaped by

| Fact | Source |
|---|---|
| Device owns the turn boundary; one tap, one turn, mic gated | `docs/adr/0007-device-owned-turn-boundary-batch-stt-for-manual-talk.md` |
| Current STT is `gemini-3.5-transcribe-live`, ~540 ms tap-to-final in isolation | benchmark 2026-09-02, `feat/gemini-transcribe-stt` |
| Child speech already broke streaming VAD: 1500 ms silence window needed, `stt_first_final_ms=12062` | ADR 0007; `docs/sarvam-stt-vad-findings.md` |
| TEN VAD is in-process via cgo, 16 kHz, 256-sample hop | `pkg/voice/vad/ten_vad.go`; `third_party/ten-vad/lib/Linux/x64/libten_vad.so` is 313 KB |
| Device is paired to a specific kid; kid id keys memory, quizzes, workspace, gallery | `docs/mobile-active-child-selection.md` |

That last row is the important one. The question "is this the kid or the parent" is not
open-ended: there is exactly one expected speaker, known in advance.

---

## 3. Three different problems, routinely conflated

The feature request assumes diarization answers the question. It does not.

**Diarization — "who spoke when."** Anonymous clustering of a continuous stream into
speaker turns, labelled `spk_1`, `spk_2`. The labels carry no identity and no ordering
guarantee across requests: `spk_1` in one clip has nothing to do with `spk_1` in the next.
It tells you *that* two people spoke. It cannot tell you *which one is the child*. On a
push-to-talk clip with one speaker it returns one cluster and zero information.

**Speaker identification / verification — "is this Aarav."** Matches audio against a
previously **enrolled voiceprint**. This does answer the question, precisely. It also
requires storing a biometric template per child, which is where the legal problem lives
(§8), and it degrades on children whose voices change month to month (§5).

**Age-group classification — "does this sound like a child."** A per-utterance classifier
over acoustic properties (F0, formants, spectral statistics). No enrollment, no stored
identifier, no per-child state. This is the only one of the three that matches both the
question and the PTT architecture — and it is the one no STT vendor sells (§4.3).

The architectural point: **diarization is a segmentation problem; this is a classification
problem.** ADR 0007 removed the continuous stream that diarization exists to segment.

---

## 4. Vendor capability, verified

### 4.1 Gemini

Both halves of the earlier reading are **confirmed**.

`gemini-3.5-transcribe-live` — diarization **not supported**:

> "Speaker diarization is not supported in live streaming sessions."
> "Word-level timestamps are not supported over the Live API."
> — [Live transcription with Gemini Live API](https://ai.google.dev/gemini-api/docs/live-api/live-transcribe)

The model page states the same split: diarization and word timestamps are supported on the
file/unary variant only, not the live one
([Gemini 3.5 Transcribe model page](https://ai.google.dev/gemini-api/docs/models/gemini-3.5-transcribe)).
Live sessions are also capped at 10 minutes.

`gemini-3.5-transcribe` (batch/unary) — diarization **supported**, and the annotation shape
is exactly as recalled:

```json
{ "type": "word_info", "text": "Hello", "speaker": "spk_1",
  "start_offset": "0.100s", "end_offset": "0.450s" }
```
— [Audio transcription](https://ai.google.dev/gemini-api/docs/transcribe)

Enabled with `"diarization_mode": "speaker"`; word timing with
`"timestamp_granularities": ["word"]`. Constraints from the same pages:

- Up to 8 speakers; "Attribution for 3+ speakers is experimental."
- Incompatible with `custom_vocabulary`.
- Incompatible with `"smart"` mode — which is the mode the 2026-09-02 benchmark credited
  with *correcting* its own partials on Cheeko's audio. Turning diarization on would give
  up that behaviour.
- Word timestamps "Degrades transcription accuracy."
- Audio cap drops from 1 hour to 30 minutes when diarization or timestamps are on.

There is a live report that the documented combination of `custom_vocabulary` +
`diarization_mode` + `timestamp_granularities` is **rejected** by the Developer API despite
being shown together in the docs
([Google AI Developers Forum](https://discuss.ai.google.dev/t/gemini-3-5-transcribe-documented-custom-vocabulary-diarization-timestamps-configuration-is-rejected-by-the-interactions-api/180240)).
Forum post, not vendor doc — treat as a flag to test, not a fact.

**Pricing** ([Gemini API pricing](https://ai.google.dev/gemini-api/docs/pricing)):
`gemini-3.5-transcribe` $2.00 /M audio tokens or $0.003/min; `gemini-3.5-transcribe-live`
$3.50 or $0.005/min.

**Relevant aside:** general Gemini models accept audio and the docs list "Emotion detection
in speech and music" and "Speaker diarization" among audio use cases, at "32 tokens per
second of audio" ([Audio understanding](https://ai.google.dev/gemini-api/docs/audio)). So a
general model *can be asked* about speaker properties. The docs do not claim age
classification and publish no accuracy figure for it — see §9.

### 4.2 Sarvam

Diarization is **batch-only**, unambiguously:

> "Batch API only: Speaker diarization is only available through the Batch API, not the
> REST or Streaming APIs."
> — [How to enable speaker diarization](https://docs.sarvam.ai/api/api-guides-tutorials/speech-to-text/how-to/enable-speaker-diarization)

The API comparison table
([Which Speech-to-Text API to Use](https://docs.sarvam.ai/api/api-guides-tutorials/speech-to-text/which-api-to-use))
confirms it and gives the latency answer:

| | REST | Realtime | Batch |
|---|---|---|---|
| Endpoint | `POST /speech-to-text` | `GET /speech-to-text-realtime/ws` | `POST /speech-to-text/job/v1` |
| Max audio | 30 s | continuous | 2 hours/file |
| **Diarization** | **No** | **No** | **Yes** |
| Latency | one round trip after upload | lowest, partials while streaming | **"Highest — minutes, depending on queue and duration"** |

So the ₹45/hour diarization line on the
[pricing page](https://docs.sarvam.ai/api-reference-docs/pricing) (vs ₹30/hour without) buys
an **asynchronous job**, not a faster path. Parameters are `with_diarization` (bool) and
optional `num_speakers` (1–20); the response adds `diarized_transcript` entries with
`transcript`, `start_time_seconds`, `end_time_seconds`, `speaker_id`. Timestamps are
chunk-level, not word-level.

**Answering the brief's Sarvam questions directly:**

- *Realtime or batch?* Batch only.
- *Latency added?* Not "added" — it is a different API with a different latency class,
  measured in minutes with queueing. Against a ~540 ms budget this is not a latency
  question, it is an architecture question. Any use is necessarily offline/analytics.
- *Which Indian languages?* **Not separately documented.** The diarization page shows only
  `hi-IN` and `en-IN` in examples and gives no per-language support matrix; the platform
  claims 22 Indian languages for Saaras v3
  ([STT overview](https://docs.sarvam.ai/api/api-guides-tutorials/speech-to-text/overview)).
  Whether diarization quality holds across all 22 is **UNVERIFIED** (§9).
- *Does it work on 2–3 second utterances?* **UNVERIFIED.** No minimum duration is
  documented. Note the intended use cases named in the docs are meetings, interviews,
  podcasts and call-centre recordings — none of which are 3-second clips.

### 4.3 Nobody sells child-vs-adult

A scan of the major speech vendors' documentation surfaced diarization and speaker
identification, but **no documented speaker-age or child/adult classification endpoint**
from Azure Speech, Google Cloud STT, AssemblyAI, Deepgram or ElevenLabs. If you want this
signal from an API, you are either asking a general multimodal model for it (unbenchmarked)
or running your own classifier.

---

## 5. Acoustics and classifier feasibility

### 5.1 The textbook F0 ranges are optimistic, and they break exactly where it matters

The figures in the request (adult male 85–180 Hz, adult female 165–255 Hz, children
250–400 Hz) are the textbook ranges. I could not find a primary measurement study that
produces exactly those bounds — treat their provenance as **UNVERIFIED**. Measured against
real corpora they are *directionally* right, but narrower in the middle and wider in the
tails than reality, and they hide the thing that decides this feature.

**Measured adult F0, spontaneous speech** (Buckeye corpus, 40 American speakers, ~1.8M
frames; *Phonetics and Speech Sciences*,
[article](https://www.eksss.org/archive/view_article?pid=pss-16-1-11)):

| | Median | Mean | SD | Range |
|---|---|---|---|---|
| Adult male | 113 Hz | 116 | 28 | 75–247 |
| Adult female | **181 Hz** | 185 | **43** | 75–395 |

So the "165–255 Hz adult female" band is both too high and too narrow. At mean 185 with
SD 43, an ordinary woman two SD up sits at ~271 Hz — inside the "children" band. Individual
speakers overlap outright: the study reports one male speaker with a median of 163 Hz and one
female at 116 Hz.

Primary source: Lee, Potamianos & Narayanan, "Acoustics of children's speech: Developmental
changes of temporal and spectral parameters", *J. Acoust. Soc. Am.* 105(3):1455–68 (1999),
[PDF](https://sail.usc.edu/care/pdfs/lee1999acoustics.pdf),
[PubMed](https://pubmed.ncbi.nlm.nih.gov/10089598/). 436 children ages 5–18 (1-year
resolution) plus 56 adults aged 25–50.

Measured mean F0, quoted from the paper:

| Speaker | Mean F0 |
|---|---|
| Male, age 5–6 | ~256–290 Hz (per-vowel means, Tables II–III) |
| Male, age 12 | **226 Hz** |
| Male, age 15 | **127 Hz** |
| Female, age 7 | **275 Hz** |
| Female, age 12 | **231 Hz** |
| Female, after age 12 | "no significant pitch change after that age" |

> "About a 78% drop in F0 occurs between age 12 (F0=226 Hz) and age 15 (F0=127 Hz) in male
> speakers, and there is no significant pitch change after age 15."
> "For female speakers, multiple comparisons indicate that the pitch drop from age 7
> (F0=275 Hz) to age 12 (F0=231 Hz) is significant, and there is no significant pitch change
> after that age."

**This is where it breaks, and it is worse than "substantial overlap".** Female F0 stops
changing significantly at about age 12, at ~231 Hz. Averaging the paper's own per-vowel
adult-female row gives **~227 Hz** — so within this corpus, *a girl of 11–12 and an adult
woman have the same mean F0, and there is no pitch gap left for a classifier to find.*
(That adult figure is derived from Table III rather than quoted from the prose; see §9.)
Girls average 251 Hz at 11, 234 at 12, 247 at 13, 226 at 14 — oscillating around the adult
value, not approaching it from above. The abstract puts the wider version of this plainly:

> "Differentiation of male and female fundamental frequency and formant frequency patterns
> begins at around age 11, becoming fully established around age 15."

So an F0-based child/adult classifier has a hard ceiling: it can separate a 6-year-old from
an adult easily, degrades through 9–12, and **cannot work at all for girls from about age
11**. If Cheeko's user base is under about 10, that ceiling may not bind. If it extends to
pre-teens, it does. Note also that DPDP defines "child" as under 18 (§8.1), so the legal
population and the acoustically-separable population are not the same set.

### 5.2 Young children are also the noisiest signal

The same paper repeatedly finds that magnitude *and within-subject variability* of acoustic
parameters decrease with age, converging to adult levels around 12 for duration and "about 2
or 3 years later" for F0 and formant variability. That is the acoustic statement of the
problem ADR 0007 already hit operationally: a six-year-old is not a small adult, and the
young end of the range is simultaneously the most separable *on average* and the most
variable *per utterance*.

**Read the corpus conditions before transferring any of these numbers.** Lee et al. recorded
scripted target words and five fixed sentences, in a sound-treated booth, with a Brüel & Kjær
4179 microphone, in American English, mostly Midwestern US speakers. Cheeko is spontaneous
Hinglish from a child holding a toy in a living room. Every accuracy figure in this section
is an upper bound, and ADR 0007 is the record of what happened last time a vendor's
adult-speech numbers met a real six-year-old.

**How large that penalty is, measured.** Kid-Whisper
([arXiv:2309.07927](https://arxiv.org/abs/2309.07927), Table 3) reports zero-shot Whisper
Large-V2 WER of **2.82% on LibriSpeech test-clean vs 29.39% on spontaneous CSLU Kids** —
roughly **10x worse** on spontaneous child speech than on adult read speech, with MyST at
12.80%. The authors put it plainly: CSLU Kids scripted WER is "between 6 and 10 times that
of Librispeech". Whatever number a paper or vendor quotes for adults, assume a multiple of
it for a six-year-old, and assume the multiple is larger for spontaneous speech than for
scripted.

### 5.3 Enrollment fails on children for a technical reason too

Shetty, Zheng, Lulich & Alwan, "Enhancing Age-Related Robustness in Children Speaker
Verification", [arXiv:2502.10511](https://arxiv.org/abs/2502.10511), measured speaker
verification on children both *within* a year and *across* years, using CSLU Kids and a
longitudinal Indiana University set. Equal Error Rates from their Table 2:

| Condition | EER range across systems |
|---|---|
| CSLU intra-year, kindergarten (K00) | 9.55 – 13.2 % |
| CSLU intra-year, grade 9 (K09) | 1.7 – 3.45 % |
| IU intra-year (same year) | 8.1 – 22.5 % |
| **IU inter-year (1–3 year gap)** | **16.8 – 40.3 %** |

Two conclusions, both decisive:

1. **Verification is worst on the youngest children.** Kindergarten EER is roughly 3–6x
   grade-9 EER on the same system and corpus. Cheeko's core users sit at the bad end.
2. **An enrolled child voiceprint decays fast, and the decay is the whole story.** Reading
   the IU baseline row as an enrollment-ageing curve: enroll in Grade 1 and verify in
   Grade 1 → **22.5% EER**; verify the same child in Grade 2 → **29.5%**; Grade 3 →
   **32.8%**; Grade 4 → **34.7%**. The G2→G4 pair is **40.3%** — worse than a coin flip
   would be embarrassing, and 40% EER is not far off it. Their combined method reduces
   average EER by "19.4%, 13.0%, and 6.1% in the one-year, two-year, and three-year gap
   inter-year evaluation sets" — note the benefit *shrinks* as the gap widens, i.e. nobody
   has solved multi-year drift. The paper opens by naming the mechanism: *"One of the main
   challenges in children's speaker verification (C-SV) is the significant change in
   children's voices as they grow."*

A related nuance worth not overstating: adult-trained SV is not uniformly catastrophic on
children. A shared-embedding study
([NoDaLiDa 2021](https://aclanthology.org/2021.nodalida-main.9.pdf)) found a VoxCeleb2-trained
model scored 2.58% EER on PF-STAR children vs 2.37% on adults — nearly no penalty — but
**10.68% on Finnish children's Speechdat, 4.5x worse**, and mixing child data into training
halved child EER with no adult degradation. So the *cross-sectional* problem is tractable
with the right data. It is the *longitudinal* problem above that is not, and enrollment is
inherently longitudinal.

**No paper prescribes a re-enrollment interval.** The IU data is the closest evidence and it
shows material degradation already at a one-year gap, which implies at least annual
re-enrollment. Any specific interval is unverified (§9).

For the product this means enrollment is not a one-time capture. It is a recurring
re-enrollment obligation on a growing child, which is more consent events, more collection,
and more retention — the opposite of what §8 wants.

### 5.4 What accuracy to actually expect

Published figures for this task span roughly 48% to 97%, and **the spread is almost entirely
explained by corpus conditions, not by model quality.** Sorting them by how much they
transfer to a toy in a living room:

**Spontaneous child–adult interaction — the closest analogue to Cheeko, and the honest
number.** USC SAIL, "Robust Self-Supervised Speech Embeddings for Child-Adult Classification
in Interactions involving Children with Autism"
([arXiv:2307.16398](https://arxiv.org/abs/2307.16398)): real clinical sessions, children
3–13, spontaneous and noisy with overlapping speech. **Binary child-vs-adult macro F1 tops
out at 76.66 (ADOSMod3) and 72.88 (Simons)** with the best WavLM configuration; a plain
wav2vec2 base gets 63–68. The paper also reports that pre-training gains are largest for the
*youngest* band (9.42% / 8.23% / 4.06% relative, youngest→oldest), concluding it is
"intrinsically challenging to model children of AG1 and AG2 due to the developing vocal tract
behaviors."

**Telephone-quality speech, classical vs deep.** INTERSPEECH 2010 Paralinguistic Challenge on
the aGender corpus
([Schuller et al.](https://www.isca-archive.org/interspeech_2010/schuller10b_interspeech.html)):
a linear SVM over 1,582 openSMILE features scored **81.21% UA** on the 3-way
`{child, female, male}` task — which is exactly the child-vs-adult question — and only
**48.83% UA** on 4-way age. Burkhardt et al. 2023
([arXiv:2306.16962](https://arxiv.org/abs/2306.16962)), the paper behind the audEERING
models, reports on the same test set that a 0.3B wav2vec2 lifts 3-way gender to **.87 UAR**
and 4-way age to **.61 UAR**. Two things follow: the child/adult cut is far easier than
fine-grained age, and **a 300M-parameter transformer buys only ~4–6 points over classical
features** on this specific task. That is the strongest argument for a small-footprint
approach (§6).

**Read speech in a quiet room — discount these.** "Layer-Wise Analysis of Self-Supervised
Representations for Age and Gender Classification in Children's Speech"
([arXiv:2508.10332](https://arxiv.org/abs/2508.10332)) reports 97.14% age accuracy on CMU
Kids and 95.00% on PF-STAR. Three reasons these do not apply: it is age classification
*within children only* (no adults in either corpus); CMU Kids is a "70/30 split" of
utterances with no stated speaker-disjointness, so the same speakers likely appear in train
and test and the model can memorise speaker identity; and both corpora are scripted school
recordings. Treat 97% as a speaker-dependent ceiling on easy audio, not a generalisation
estimate.

**Short utterances.** The cleanest controlled comparison is the Lahjoita puhetta benchmarks
([arXiv:2203.12906](https://arxiv.org/pdf/2203.12906), Table 9), spontaneous Finnish: matched
age-classification models scored **33.59% on ≤3 s segments vs 42.39% on ≤50 s** — the authors
conclude "more information is required for the model to learn this task." A later paper
claims 1–3 s suffices, but on read-speech child-only benchmarks. **Read: 2–3 s is workable
for a *binary* decision and marginal for anything finer, and accumulating over several turns
will beat any single 2-second window.**

**Under 6: no evidence.** I found no paper reporting child-vs-adult accuracy isolated to
children under 6. The nearest is the SAIL youngest band (43–90 months), which is the hardest
of their three. This is Cheeko's core population and it is an evidence gap (§9).

### 5.5 Off-the-shelf models

The most-downloaded option is
[`audeering/wav2vec2-large-robust-24-ft-age-gender`](https://huggingface.co/audeering/wav2vec2-large-robust-24-ft-age-gender)
(2.0M downloads). Its model card is directly disqualifying on two counts:

- **License: CC-BY-NC-SA-4.0.** Non-commercial. Cheeko is a commercial product; audEERING's
  card directs commercial users to a separate devAIce license.
- **Size: 0.3B parameters.** Not an on-device model by any reading of §6. (The 6-layer
  variant is 90.8M / 363 MB, same license.)

It outputs a `child / female / male` head plus a 0–100 age estimate, trained on aGender,
Common Voice, TIMIT and VoxCeleb2. **None of those contains young children** — aGender's
CHILD class starts at age 7 and Common Voice's youngest bin is "teens (<19)". The most
popular open age model has essentially never seen a five-year-old.

There is a permissively-licensed alternative built for exactly this binary:

| Model | Params | Size | License | Claimed accuracy |
|---|---|---|---|---|
| [`bookbot/distil-wav2vec2-adult-child-cls-37m`](https://huggingface.co/bookbot/distil-wav2vec2-adult-child-cls-37m) | 37.9M | 151.5 MB | **Apache-2.0** | 95.89%, F1 0.9624 |
| [`bookbot/wav2vec2-adult-child-cls`](https://huggingface.co/bookbot/wav2vec2-adult-child-cls) | 94.6M | 378.3 MB | **Apache-2.0** | 95.80%, F1 0.9618 |
| [`bookbot/distil-wav2vec2-xls-r-adult-child-cls-64m`](https://huggingface.co/bookbot/distil-wav2vec2-xls-r-adult-child-cls-64m) | 63.8M | 255.1 MB | **Apache-2.0** | 93.86%, F1 0.9425 |

**Do not take those accuracy figures at face value.** The cards describe training on "a
private adult/child speech classification dataset" with no age range, no size, and no
independent test set, and the quoted number is the **final-epoch validation accuracy**. The
license is genuinely usable; the accuracy claim is not externally verifiable and must be
re-measured on Cheeko's own audio (§9, §10).

**Dataset licensing is the structural blocker for training your own.** The large, usable
child corpora are non-commercial: MyST ([LDC2021S05](https://catalog.ldc.upenn.edu/LDC2021S05),
~470 h, grades 3–5, spontaneous) is CC BY-NC-SA 4.0 with a separate commercial license via
Boulder Learning; CHILDES/TalkBank is CC BY-NC-SA 3.0 and its
[ground rules](https://talkbank.org/0share/rules.html) state the license "precludes the
incorporation of the data in commercial products, including systems such as large language
models". CSLU Kids and CMU Kids are LDC-gated non-commercial agreements; aGender is ELRA with
a license "to be negotiated". Common Voice is CC0 but has no young-child data at all.
**There is no clean commercial path from public child-speech data** — which is precisely the
pressure that produced the Alexa fact pattern in §8.2.

---

## 6. On-device feasibility

The budget is the constraint. Tap-to-final is ~540 ms measured in isolation. Anything on the
critical path that adds hundreds of milliseconds is a visible regression to a child waiting
for a toy to answer.

**What "fits" looks like here.** TEN VAD is the existing reference point:
`third_party/ten-vad/lib/Linux/x64/libten_vad.so` is 313 KB on disk, and the upstream repo
reports RTF between 0.0050 (iPhone 8) and 0.0570 (Android Galaxy J6+)
([TEN-framework/ten-vad](https://github.com/TEN-framework/ten-vad)). Licensing is
"Apache 2.0 with additional conditions" — worth actually reading the LICENSE file before
assuming a second model can ship on the same terms. Note the repo lists Linux/Windows/macOS/
Web/Android/iOS and **no MCU target**: TEN VAD does not run on an ESP32 either, so "put it
on the toy" is not a smaller version of the existing pattern, it is a new platform.

**The realistic on-device options, in order of cost:**

1. **Classical features into a small classifier.** F0 statistics (mean, median, range),
   formant estimates, and MFCC summary statistics into a GBM or SVM. Kilobytes of model,
   sub-millisecond inference, no license question. Notably, TEN VAD already contains a pitch
   estimator (`pitch_est.cc`, modified LPCNet BSD code per the upstream repo), so F0 is
   already being computed in the audio path — a classifier over it is close to free
   computationally. **This is better supported by the literature than it sounds:** the
   INTERSPEECH 2010 openSMILE+SVM baseline reached 81.21% UA on 3-way
   `{child, female, male}` telephone speech, within ~6 points of a 0.3B transformer on the
   same data (§5.4). §5.1 still caps what any F0-led method can achieve, and accuracy on
   spontaneous child Hinglish is unverified (§9).
2. **A small pretrained binary classifier, server-side.**
   `bookbot/distil-wav2vec2-adult-child-cls-37m` is 37.9M params / 151.5 MB, **Apache-2.0**,
   and purpose-built for this exact binary — CPU-runnable in or beside the Go worker. Its
   published accuracy is validation-only on undisclosed private data (§5.5), so it is a
   candidate to *evaluate*, not to trust.
3. **A neural classifier trained in-house.** Feasible, but needs labelled child and adult
   audio from the actual device, and the public child corpora that would supplement it are
   non-commercial (§5.5). That makes it a data-collection programme with its own §8 consent
   problem — collecting and retaining child voice recordings to improve a model is the Alexa
   fact pattern.
4. **A large pretrained model (audEERING-class).** 0.3B params, non-commercial license.
   Not on-device, not licensable as-is.
5. **ESP32 in the toy.** There is a real existence proof: *LimitAccess*
   ([Discover Artificial Intelligence 3:8, 2023](https://doi.org/10.1007/s44163-023-00051-x))
   runs an int8-quantized 1-D CNN over MFCCs on an Arduino Nano 33 BLE Sense (nRF52840,
   1 MB flash / 256 KB RAM) and reports **85.89% accuracy / 87.7% F1**, with child recall
   84.9% and adult recall 89.4%. Three caveats that matter more than the headline: it
   classifies **one fixed keyword**, not open speech; it was trained on 40 minutes of
   self-collected audio; and the authors state plainly that the model *"sometimes has
   difficulty distinguishing female adults from children"* and that they excluded ages 13–16
   from the child class — an independent, empirical confirmation of the F0/formant overlap in
   §5.1. Separately, the firmware already carries the PTT contract across three repos and
   ADR 0007 notes reversing it is costly. Most expensive option here, least marginal benefit.

**The cheapest option that is not on-device at all:** the general Gemini models accept audio
at "32 tokens per second of audio" and the docs list emotion detection and speaker
characteristics among audio use cases
([Audio understanding](https://ai.google.dev/gemini-api/docs/audio)). A 3-second PTT clip is
~96 audio tokens; at `gemini-3.5-flash` audio input pricing of $1.00 per million tokens
([pricing](https://ai.google.dev/gemini-api/docs/pricing)) that is on the order of $0.0001
per turn — negligible. Run **off the critical path**, after the response has been dispatched,
and it costs zero user-visible latency. Accuracy is entirely unbenchmarked for this task
(§9), so this is a cheap experiment, not a solution.

---

## 7. Options compared

Latency is against the ~540 ms tap-to-final budget. "Legal exposure" is elaborated in §8.

| Option | Answers the question? | Latency | Cost | Accuracy | Legal exposure |
|---|---|---|---|---|---|
| **A. PTT + device→kid pairing (status quo)** | Yes, by construction — one expected speaker, known in advance | 0 ms | ₹0 | Wrong only when someone other than the paired child taps the toy; frequency unmeasured, see §9 | None new. No voice data derived, nothing stored |
| **B. Gemini batch diarization** (`gemini-3.5-transcribe`) | **No** — anonymous `spk_N` labels, and a PTT clip has one speaker | Second call, off-path only; also forfeits `smart` mode and costs transcription accuracy if word timestamps are on | $0.003/min on top of the live call | N/A for this question | Low-moderate: still just audio processing, but a second copy of child audio to a second endpoint |
| **C. Sarvam batch diarization** | **No** — same reason | **Minutes**, async job with queueing. Not a real-time option at all | ₹45/hr vs ₹30/hr | N/A for this question | Same as B, plus a job store holding child audio |
| **D. Stateless child/adult classifier, in the worker** | Partly — child vs adult, not "which person" | Sub-ms if classical; ~tens of ms for a 38M model (§6) | Engineering only | **~73–77 macro F1 on spontaneous child–adult audio** (§5.4); ~81–87% UA on telephone speech. Ceiling from §5.1: good under ~9, degrades 9–12, **fails for girls 11+**. Unverified on Hinglish | Lowest of the "new signal" options if nothing is stored. Still needs a DPDP §9(3) argument |
| **E. Stateless classification via a general Gemini call, off-path** | Partly — same as D | 0 ms user-visible if fired after dispatch | ~$0.0001/turn | **Unverified** — no published benchmark for this task | Same as D, plus the clip goes to a general model as well |
| **F. Enrolled voiceprint (speaker verification)** | Yes, precisely — this is the only option that truly identifies | ~100s of ms plus enrollment UX | Engineering + storage + consent flow + re-enrollment | **Enrollment decays: 22.5% EER same-year → 29.5% at +1yr → 34.7% at +3yr; 9.6–13.2% EER even same-year on 5-year-olds** (§5.3) | **Highest.** Voiceprint is a named biometric identifier under COPPA §312.2(10); breaks the §312.5(c)(9) exception; full DPDP §9(1) consent; deletion reaches derived models |

The shape of the table is the argument. The two options that would answer the question
exactly are A (free, already built) and F (expensive, legally hazardous, and technically
poor on exactly this population). B and C do not answer it at all. D and E answer a weaker
version of it, cheaply, with a real accuracy ceiling.

---

## 8. Privacy and law

This is the section that decides the feature. A design that cannot ship to children is not
a design.

### 8.1 India — DPDP Act 2023

Worked from the gazette text
([DPDP Act 2023, No. 22 of 2023](https://www.meity.gov.in/static/uploads/2024/06/2bf1f0e9f04e6fb4f8fef35e82c42aa5.pdf)).

**"Child" means under 18.** §2(f): *"'child' means an individual who has not completed the
age of eighteen years"*. Not 13. Every Cheeko user is a child under this Act, and §9 applies
to the entire user base — there is no COPPA-style age cutoff that lets older users out.

**§9(1) — verifiable parental consent, before any processing:**

> "The Data Fiduciary shall, before processing any personal data of a child or a person with
> disability who has a lawful guardian obtain verifiable consent of the parent of such child
> or the lawful guardian, as the case may be, in such manner as may be prescribed."

**§9(2) — no detrimental processing:**

> "A Data Fiduciary shall not undertake such processing of personal data that is likely to
> cause any detrimental effect on the well-being of a child."

**§9(3) — the hard prohibition:**

> "A Data Fiduciary shall not undertake tracking or behavioural monitoring of children or
> targeted advertising directed at children."

Three things about §9(3) matter here, and they are the crux of the legal analysis:

1. **It is not consent-waivable.** §9(1) is a consent obligation; §9(3) is a flat
   prohibition. Parental consent does not cure it. The only relief is §9(4) — exemption for
   "such classes of Data Fiduciaries or for such purposes... as may be prescribed" — i.e.
   you would need to fall inside a class the government has actually prescribed.
2. **"Tracking" and "behavioural monitoring" are undefined in the Act.** I checked the full
   gazette text: the words "tracking" and "monitoring" occur exactly once each, inside
   §9(3) itself. The word "profiling" does not appear at all. The prohibition's scope is
   therefore set by rules and by regulator practice, not by the statute — which makes
   anything near the line a risk to be argued, not a box to be ticked.
3. **The stated feature intent includes "analytics" and "parental context".** Building a
   per-child behavioural record from voice, keyed to a persistent child identity, and
   surfacing it to parents as insight, is close enough to "behavioural monitoring of
   children" that it needs a legal opinion before engineering time, not after. Note this
   risk attaches to the *product feature*, largely independent of which technique
   implements it.

**No biometric category — and that cuts both ways.** I searched the full Act text: the words
"biometric" and "sensitive" do not appear. DPDP has no special category of sensitive or
biometric data (unlike GDPR Art. 9 or the old SPDI Rules). So a stored child voiceprint is
**not** subject to a heightened biometric regime — it is simply "personal data" of a child,
which means the whole of §9 applies to it, including §9(3). The absence of a biometric
category removes an extra hurdle; it does not create a safe harbour.

**Deletion obligations that a voiceprint store would inherit:**

- §8(7)(a): erase personal data on withdrawal of consent or "as soon as it is reasonable to
  assume that the specified purpose is no longer being served, whichever is earlier";
  §8(7)(b) requires causing your processors to erase too.
- §12(3): the Data Principal can request erasure and you must comply absent a legal
  retention need.

For a voiceprint, "erase" has to mean the enrolled template, every derived embedding, any
model fine-tuned on it, and the copies at every processor. That is a materially harder
delete than a text row.

**Penalty.** The Schedule (see §33(1)) lists "Breach in observance of additional obligations
in relation to children under section 9" with a penalty that "May extend to two hundred
crore rupees" — ₹200 crore, second only to the security-safeguards entry at ₹250 crore.

**Cross-border.** §16(1) gives the Central Government power to restrict transfers to notified
countries or territories. Not a blanket localisation mandate today, but a standing power
that a US-hosted voiceprint store would sit under.

**DPDP Rules 2025 — see §9.** The mechanics of "verifiable consent" (Rule 10) live in the
Rules, not the Act. I could not retrieve the notified Rules from a primary source
(meity.gov.in serves a client-rendered page and blocks direct PDF fetches). Treat all
Rule-level detail as unverified.

### 8.2 United States — COPPA, if they sell there

Worked from the current regulatory text at
[16 CFR Part 312 (eCFR)](https://www.ecfr.gov/current/title-16/chapter-I/subchapter-C/part-312).

**The raw audio is already personal information.** § 312.2, "Personal information", clause
(8): *"A photograph, video, or audio file where such file contains a child's image or
voice"*.

**Voiceprints are named explicitly.** Clause (10), added by the 2025 amendments:

> "A biometric identifier that can be used for the automated or semi-automated recognition
> of an individual, such as fingerprints; handprints; retina patterns; iris patterns;
> genetic data, including a DNA sequence; **voiceprints**; gait patterns; facial templates;
> or faceprints"

The amended Rule is effective 23 June 2025 with a compliance date of 22 April 2026
([FTC press release, Jan 2025](https://www.ftc.gov/news-events/news/press-releases/2025/01/ftc-finalizes-changes-childrens-privacy-rule-limiting-companies-ability-monetize-kids-data)).
That date has passed.

**The exception a voice toy currently lives inside — and how enrollment destroys it.**
§ 312.5(c)(9) exempts from prior parental consent:

> "Where an operator collects an audio file containing a child's voice, **and no other
> personal information**, for use in responding to a child's specific request and where the
> operator does not use such information for any other purpose, does not disclose it, and
> deletes it immediately after responding to the child's request. In such case, there also
> shall be no obligation to provide a direct notice, but notice shall be required under
> § 312.4(d)."

§ 312.4(d)(4) requires the online notice to describe the use and state "that the operator
deletes such audio files immediately after responding to the request".

Read those together against the feature:

- **A stateless per-turn child/adult classifier** that consumes the clip, emits a boolean,
  and discards the audio and any intermediate embedding is *arguably* still inside the
  exception — a child/adult flag cannot be "used for the automated or semi-automated
  recognition of an individual", so it is not a clause-(10) biometric identifier. This is my
  reading of the rule text, not a verified FTC position (§9), and "for any other purpose" is
  the phrase counsel would have to get comfortable with, since classification is arguably a
  purpose beyond responding to the request.
- **Enrollment breaks the exception outright.** A stored voiceprint is by definition "other
  personal information" collected from the audio. Once you hold one you are outside
  § 312.5(c)(9) and into full verifiable parental consent under § 312.5, direct notice under
  § 312.4(b)–(c), and parental review/deletion rights under § 312.6.
- **You also cannot keep it.** § 312.10: retain "only as long as is reasonably necessary to
  fulfill the specific purpose(s)"; "Personal information collected online from a child may
  not be retained indefinitely"; and you must maintain a written retention policy stating
  the business need and a deletion timeframe. A voiceprint whose entire value is persistence
  across months sits awkwardly against a rule that forbids indefinite retention.

**The enforcement precedent is directly on point.** In *US v. Amazon.com (Alexa)* (2023) the
FTC and DOJ charged Amazon with retaining children's Alexa voice recordings indefinitely and
undermining parental deletion requests; the FTC's own account notes Amazon's justification
that "Children's speech patterns and accents differ from those of adults, so the unlawfully
retained voice recordings provided Amazon with a valuable database for training the Alexa
algorithm to understand children." The settlement carried a **$25 million civil penalty**,
required deletion of certain voice recordings and inactive child accounts, and **prohibited
Amazon from using such data to train its algorithms**
([FTC press release](https://www.ftc.gov/news-events/news/press-releases/2023/05/ftc-doj-charge-amazon-violating-childrens-privacy-law-keeping-kids-alexa-voice-recordings-forever);
[case page](https://www.ftc.gov/legal-library/browse/cases-proceedings/192-3128-amazoncom-alexa-us-v)).

That is the exact fact pattern of "keep child voice data because child speech is hard and it
will make our model better" — which is precisely the argument that would be made internally
for enrollment or for retaining a training corpus.

### 8.3 What makes enrollment expensive, concretely

Independent of accuracy, enrolling a child voiceprint costs you:

- **A consent flow, per child, verifiable to the parent.** DPDP §9(1) plus, in the US, full
  § 312.5 verifiable parental consent for the biometric.
- **Deletion machinery that reaches derived artifacts.** DPDP §8(7)/§12(3) and COPPA
  § 312.10 all require real deletion; the Alexa order shows regulators will reach models
  trained on the data.
- **A retention policy you can defend in writing**, with a deletion timeframe (§ 312.10),
  for data whose value is that it does not get deleted.
- **Re-enrollment as a recurring product task**, at least annually on the §5.3 evidence,
  because the voiceprint decays as the child grows — meaning repeated collection and repeated
  consent, not a one-time capture. Every re-enrollment is a fresh COPPA collection event.
- **A training-data problem with no clean commercial answer.** The public child-speech
  corpora that would make any of this work are non-commercial (§5.5), which is exactly the
  pressure that led to the Alexa retention conduct above.
- **A DPDP §9(3) argument** for why a persistent per-child voice identity used for "analytics"
  is not behavioural monitoring, against a term the statute leaves undefined.
- **Data-residency optionality** you may lose later under DPDP §16(1).

Against that, the benefit is disambiguating a speaker you already know from the device
pairing. The cost/benefit is not close.

---

## 9. What is unverified

Listed honestly, with how to close each one. A clearly-labelled gap is more useful than a
confident guess.

| # | Claim I could not verify | Why | How to verify |
|---|---|---|---|
| 1 | **DPDP Rules 2025 mechanics** — Rule 10 verifiable-consent methods, the exemption schedule under §9(4), and the in-force dates | meity.gov.in serves a client-rendered page and returns 403 on direct PDF fetch; every readable account I found was a law-firm or news summary, not the Gazette | Get the Gazette notification PDF (e-Gazette, or MeitY's document portal in a real browser) and read Rule 10 and the Schedules directly. **Do not act on the secondary summaries** — they disagree with each other on dates |
| 2 | Whether **"tracking or behavioural monitoring"** in DPDP §9(3) reaches per-turn speaker classification or parent-facing analytics | The Act defines neither term (verified: each word appears exactly once, inside §9(3)) | Indian privacy counsel, against the notified Rules. This is the single question that should gate the whole feature |
| 3 | Whether a **stateless child/adult boolean** stays inside COPPA § 312.5(c)(9) | My reading of "no other personal information" and clause (10)'s "recognition of an individual" test. The FTC has published no guidance on this specific case | US privacy counsel; optionally an FTC informal staff opinion |
| 4 | **Sarvam diarization language coverage** — which of the 22 languages it actually supports | The diarization page shows only `hi-IN`/`en-IN` examples and publishes no per-language matrix | Ask Sarvam support directly; the docs will not answer it |
| 5 | **Sarvam diarization on 2–3 second clips** — minimum duration and behaviour | No minimum documented; every named use case (meetings, interviews, call centres) is long-form | Empirical: submit short single-speaker clips to the batch job API and inspect `diarized_transcript`. Also academic, since batch latency already rules it out for the live path |
| 6 | **Accuracy of asking a general Gemini model "child or adult?"** | The audio docs list emotion detection and speaker diarization as use cases but publish no age-classification benchmark, and no vendor sells this as a feature (§4.3) | Build a small labelled set from real Cheeko device audio (with consent) and measure. See §10 |
| 7 | **Any classifier's accuracy on spontaneous child Hinglish** | Every figure in §5.4 comes from English, German or Finnish corpora. No study covers this population, language and condition | Same labelled set as #6 |
| 7a | **Child-vs-adult accuracy on children under 6** | No paper reports it isolated to that band; the nearest evidence (SAIL, 43–90 months) is the hardest of three groups. This is Cheeko's core population | Same labelled set as #6, stratified by age band |
| 7b | **The Bookbot models' 95.89% claim** | Trained and validated on undisclosed private data; the quoted figure is final-epoch *validation* accuracy, not a held-out test | Run it against your own labelled set. The Apache-2.0 license is verified; the accuracy is not |
| 7c | Provenance of the textbook "85–180 / 165–255 / 250–400 Hz" ranges, and Lee's adult-female value | No primary measurement study produces exactly those bounds; the 227 Hz adult-female figure is averaged from Lee's Table III, and the table's adult row label came through PDF extraction imperfectly | Only matters if someone builds a threshold on them — in which case measure your own population instead |
| 7d | Accuracy-vs-duration curve for *binary* child/adult | The 3 s vs 50 s comparison (§5.4) is multi-class age, not binary. No binary curve published | Measure on Cheeko turns, bucketed by clip length |
| 8 | **How often a non-paired speaker actually taps the toy** | Never measured. The entire cost/benefit of this feature rests on this number and nobody has it | See §10, step 1 |
| 9 | The reported rejection of `custom_vocabulary` + `diarization_mode` + `timestamp_granularities` together | Single forum post, not vendor documentation | One API call, if you ever need that combination |
| 10 | Whether TEN VAD's "Apache 2.0 **with additional conditions**" permits what you plan | The upstream README names the deviation but the conditions are in the LICENSE file, unread | Read `LICENSE` in the ten-vad repo before adding a second bundled model on the same assumption |

Two things I deliberately did **not** claim: that any specific open model would hit a
specific accuracy on Cheeko's audio, and that a stateless classifier is definitely lawful.
Both are the sort of thing that reads well in a document and fails in front of a regulator or
a six-year-old.

---

## 10. If you proceed

Ordered so that the cheapest step that could kill the feature comes first.

1. **Measure the problem before building for it.** Instrument the existing pipeline to
   estimate how often the speaker is *not* the paired child. Cheapest version: on a sample of
   turns, off the critical path, ask a general Gemini model to label the clip child/adult and
   log only the boolean plus the turn id. Run it for a week.
   *Verify:* you have a rate. If it is under a few percent, stop — the pairing prior (option
   A) is already the right answer and there is nothing to build.
2. **In parallel, get the DPDP §9(3) question answered by counsel** (gap #2), framed as:
   "may we derive and store a per-turn speaker attribute for a child user, and surface
   aggregate insight from it to the parent?" Do this before any engineering. If the answer is
   no, steps 3–5 are moot regardless of the measured rate.
   *Verify:* a written opinion referencing the notified Rules, not the Act alone.
3. **Retrieve the DPDP Rules 2025 from the Gazette** (gap #1) and re-read §8.1 against them.
   The Act alone is not a compliance basis.
4. **Only if 1 shows a real rate and 2 comes back clear:** prototype option D or E as a
   stateless per-turn classifier, off the critical path, storing one boolean and no
   voice-derived identifier. Build the labelled evaluation set from real device audio first
   (gaps #6, #7, #7a) — stratified by age band, including under-6 and any pre-teen girls,
   which §5.1 predicts will be the failure mode. Three baselines worth measuring against each
   other on that set, cheapest first: a classical F0+MFCC classifier reusing TEN VAD's
   existing pitch estimate; `bookbot/distil-wav2vec2-adult-child-cls-37m` (Apache-2.0, 38M
   params); and a general Gemini call. Expect ~73–77 F1 as the realistic bar, not 95%.
   *Verify:* per-age-band accuracy on real Cheeko audio, not a vendor's or a model card's
   number.
5. **Do not build enrollment** unless something changes that is not in this document. The
   technical case is bad on exactly this population (~29–31% EER across a 1–3 year gap,
   9.6–13.2% EER same-year on 5-year-olds, §5.3) and the legal case is worse (§8.2, §8.3).

**What to write down afterwards.** If the team decides, that decision belongs in
`docs/adr/` as a new ADR, citing this note and ADR 0007. If the decision is "no", write it
down anyway — a recorded "we considered speaker ID and declined, for these reasons" is worth
more than silence the next time someone asks for it.
