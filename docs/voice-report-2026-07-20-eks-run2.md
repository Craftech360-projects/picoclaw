# Cheeko Voice Pipeline Report

Generated: 2026-07-20T08:38:47+00:00 UTC  
Log: `eks-logs3.txt` (full file)

## Setup

| Stage | Provider / Model | Key config |
|---|---|---|
| STT | sarvam / saaras:v3 | language=unknown (auto-detect), mode=transcribe, 16000 Hz |
| LLM | google/gemma-4-31b-it | max_tokens=260, temperature=0.7, streaming=true, tools=7 (via https://openrouter.ai/api/v1) |
| TTS | sarvam / bulbul:v3 | voice=pooja, 24000 Hz linear16, languages used: en-IN, hi-IN, ml-IN |
| VAD | TEN VAD | threshold=0.68, endpoint=700 ms |

## Volume

- Turns captured: **29** (28 user turns, 1 greetings)
- Sessions: 1
- Window: 2026-07-20T08:26:35+00:00 → 2026-07-20T08:38:03+00:00

## Latency (user turns, ms)

| Stage | n | avg | p50 | p95 | max |
|---|---|---|---|---|---|
| STT final transcript (speech start → final text) | 21 | 4288 | 2835 | 11863 | 13016 |
| LLM first token (after transcript) | 24 | 3864 | 2051 | 12812 | 26852 |
| LLM full response | 21 | 19643 | 18231 | 34251 | 44034 |
| TTS first audio (after LLM first sentence) | 24 | 4557 | 2526 | 14998 | 28447 |
| First reply audio from speech START | 17 | 7038 | 5469 | 14461 | 15982 |
| Voice-to-voice: user STOPPED → first reply audio | 12 | 20340 | 10322 | 57686 | 63128 |
| Turn end-to-end total | 28 | 19979 | 21901 | 38876 | 50130 |

## Voice-to-voice by TTS language

| Language | turns | avg ms | p95 ms |
|---|---|---|---|
| en-IN | 9 | 25883 | 63128 |
| hi-IN | 2 | 3624 | 6388 |
| ml-IN | 1 | 3889 | 3889 |

## Usage per stage

| Stage | Billing unit | Total | Avg / turn |
|---|---|---|---|
| STT (Sarvam) | audio seconds | 61.3 s | 2.2 s |
| LLM prompt | tokens | 255731 | 12178 |
| LLM completion | tokens | 1433 | 68 |
| TTS (Sarvam) | input characters | 4261 | 152 |

## Conversation log (user turns)

| # | time | lang | stt_s | llm tok in/out | tts chars | e2e ms | transcript | LLM response | TTS input |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 08:27:20 | en-IN | 1.8 | 11751/129 | 442 | 38876 | Hello, can you listen to me? | [happy] Oho! Yes, I can hear you loud and clear, Rahul! [excited] I am listening with all my super-p… | Oho! / Yes, I can hear you loud and clear, Rahul! / I am listening with all my super-powered AI ears… |
| 2 | 08:27:28 | en-IN | 2.5 | 0/0 | 0 | 4808 | How old are you? |  |  |
| 3 | 08:27:49 | en-IN | 0.0 | 11154/73 | 223 | 21867 |  | [silly] Oho! That is a tricky question! [excited] I am as old as a thousand chocolate cakes and as y… | Oho! / That is a tricky question! / I am as old as a thousand chocolate cakes and as young as a tiny… |
| 4 | 08:28:26 | en-IN | 2.6 | 11247/74 | 219 | 29925 | Okay, tell me what is 2+3? | [excited] Oho! A math challenge! [happy] Two plus three is five! [silly] I calculated it in a blink … | Oho! / A math challenge! / Two plus three is five! / I calculated it in a blink of an eye, but I alm… |
| 5 | 08:28:45 | en-IN | 3.7 | 0/0 | 0 | 13363 | What is |  |  |
| 6 | 08:28:58 | en-IN | 0.0 | 10703/56 | 163 | 13393 |  | [confused] Oho! I think we got a little bit jumbled up! [curious] You said something about a movie, … | Oho! / I think we got a little bit jumbled up! / You said something about a movie, and then you aske… |
| 7 | 08:30:07 | en-IN | 2.3 | 10776/74 | 217 | 33928 | Okay, do you like mangoes? | [excited] Oho! Do I like mangoes? [love] I LOVE them! [happy] They are the king of fruits, and they … | Oho! / Do I like mangoes? / I LOVE them! / They are the king of fruits, and they make my circuits do… |
| 8 | 08:30:14 | en-IN | 3.1 | 0/0 | 21 | 2827 | Three Animals |  | Oho! / Name what, Rahul? |
| 9 | 08:30:47 | en-IN | 0.0 | 10873/68 | 208 | 32907 |  | [excited] Oho! Three animals! [happy] Let us go with a giant elephant, a cheeky monkey, and a tiny, … | Oho! / Three animals! / Let us go with a giant elephant, a cheeky monkey, / and a tiny, cute kitten!… |
| 10 | 08:31:19 | en-IN | 2.7 | 23349/77 | 159 | 29645 | Okay, tell me what day is it today? | [happy] Oho! Today is Monday, July twentieth, twenty twenty six! [excited] It is the start of a bran… | Oho! / Today is Monday, July twentieth, twenty twenty six! / It is the start of a brand new week for… |
| 11 | 08:31:25 | en-IN | 3.3 | 0/0 | 0 | 3827 | From one to five |  |  |
| 12 | 08:31:49 | en-IN | 0.0 | 11196/58 | 160 | 24297 |  | [excited] Oho! I can definitely do that! [happy] One, two, three, four, five! [silly] That was so fa… | Oho! / I can definitely do that! / One, two, three, four, five! / That was so fast, I think I broke … |
| 13 | 08:32:00 | en-IN | 4.7 | 0/0 | 0 | 4542 | Weather condition in Bangalore right now? |  |  |
| 14 | 08:32:50 | en-IN | 0.0 | 23473/79 | 285 | 50130 |  | [happy] Oho! In Bangalore, it is twenty six degrees Celsius and there is a little bit of drizzle! [e… | Oho! / In Bangalore, it is twenty six degrees Celsius and there is a little bit of drizzle! / It is … |
| 15 | 08:32:56 | en-IN | 4.1 | 0/0 | 19 | 3815 | Sound does a dog make? |  | Hmmm, what is that? |
| 16 | 08:33:11 | en-IN | 0.0 | 10703/53 | 133 | 14869 |  | [excited] Oho! That is easy peasy! [happy] A dog goes woof woof! [silly] Sometimes they go bow wow i… | Oho! / That is easy peasy! / A dog goes woof woof! / Sometimes they go bow wow if they are feeling e… |
| 17 | 08:33:30 | hi-IN | 2.7 | 10772/57 | 142 | 16200 | दो और दो कितना होता है? | [happy] Arrey! Do aur do hote hain chaar! [excited] Two plus two is four! [silly] I am a math genius… | Arrey! / Do aur do hote hain chaar! / Two plus two is four! / I am a math genius, right? / Do you wa… |
| 18 | 08:33:50 | hi-IN | 2.3 | 0/0 | 4 | 3602 | तुम्हारा नाम क्या है? |  | Yay! |
| 19 | 08:34:04 | hi-IN | 0.0 | 10850/51 | 151 | 13849 |  | [happy] Arrey! Mera naam Cheeko hai! [excited] I am your fun and super smart best friend! [silly] Do… | Arrey! / Mera naam Cheeko hai! / I am your fun and super smart best friend! / Do you think Cheeko is… |
| 20 | 08:34:38 | hi-IN | 1.5 | 10916/58 | 170 | 23771 | आसमान का रंग। | [happy] Arrey! Aasman ka rang neela hota hai! [excited] The sky is blue! [silly] But sometimes it tu… | Arrey! / Aasman ka rang neela hota hai! / The sky is blue! / But sometimes it turns orange and pink … |
| 21 | 08:34:56 | hi-IN | 1.7 | 10989/58 | 143 | 15132 | बिल्ली कैसे बोलते हैं? | [happy] Arrey! Billi bolti hai meow meow! [excited] A cat goes meow meow! [silly] I bet they are say… | Arrey! / Billi bolti hai meow meow! / A cat goes meow meow! / I bet they are saying that they want s… |
| 22 | 08:35:14 | ml-IN | 2.9 | 11069/51 | 136 | 15069 | ഹലോ, ഞാൻ പറയുന്നത് കേൾക്കാൻ കഴിയുന്നുണ്ടോ? | [happy] Hello! [excited] Yes, I can hear you loud and clear! [curious] You are speaking Malayalam, r… | Hello! / Yes, I can hear you loud and clear! / You are speaking Malayalam, right? / It sounds so mus… |
| 23 | 08:35:44 | ml-IN | 2.7 | 10442/48 | 122 | 24890 | ഓക്കേ, രണ്ടും മൂന്നും കൂടെ കൂട്ടിയാൽ എത്ര കിട്ടും? | [happy] Oho! [excited] Two plus three is five! [silly] I counted them on my virtual fingers! [curiou… | Oho! / Two plus three is five! / I counted them on my virtual fingers! / Do you want to try a harder… |
| 24 | 08:36:19 | ml-IN | 3.2 | 11147/101 | 316 | 31161 | ഓക്കേ എനിക്കൊരു ചെറിയൊരു കഥ പറഞ്ഞു തരാമോ | [excited] Oho! A story! I love telling stories! [happy] Let us imagine a tiny, brave ant named Chint… | Oho! / A story! / I love telling stories! / Let us imagine a tiny, brave ant named Chintu who lived … |
| 25 | 08:36:43 | ml-IN | 4.1 | 11272/61 | 178 | 21901 | ഓക്കേ, ആ കഥയിലെ കഥാപാത്രത്തിന്റെ പേരെന്തായിരുന്നു? | [happy] Arrey! [excited] The brave little ant was named Chintu! [silly] Chintu is such a cute name f… | Arrey! / The brave little ant was named Chintu! / Chintu is such a cute name for a tiny hero, right?… |
| 26 | 08:37:10 | ml-IN | 3.0 | 11354/67 | 173 | 23508 | എനിക്ക് വേണ്ടി ഒരു തമാശ പറയാൻ പറ്റുമോ? | [laughing] Oho! A joke! I have a super silly one for you! [happy] Why did the tomato turn red? [sill… | Oho! / A joke! / I have a super silly one for you! / Why did the tomato turn red? / Because it saw t… |
| 27 | 08:37:36 | ml-IN | 3.4 | 10807/64 | 213 | 22528 | ഓക്കേ, നിനക്ക് ഏറ്റവും ഇഷ്ടമുള്ള നിറം ഏതാണ്? | [happy] Oho! That is a tough question! [excited] I love the color bright yellow because it looks lik… | Oho! / That is a tough question! / I love the color bright yellow because it looks like a happy sunf… |
| 28 | 08:38:03 | en-IN | 2.7 | 10888/76 | 264 | 24769 | Okay, tell me something about elephants. | [excited] Oho! Elephants are amazing! [happy] Did you know they have a super powerful nose called a … | Oho! / Elephants are amazing! / Did you know they have a super powerful nose called a trunk that the… |

Full untruncated text per turn: `turns.csv`.

---

## Findings (2026-07-20 EKS run 2, device 68ee8f60bc00, 28 user turns)

**Headline: pipeline is healthy; OpenRouter Gemma serving variance dominated latency.**
- No 429s or provider errors. LLM first token p50 2.0s but p95 12.8s / max 26.8s; full responses up to 44s.
- Turns that hit fast LLM spells achieved voice-to-voice 3.6-3.9s (hi-IN, ml-IN) — matches the DO-server baseline. Slow spells pushed en-IN turns to 10-63s.
- New metric live: "Voice-to-voice: user STOPPED → first reply audio" (true gap; the from-START row includes the user's own speaking time).

**Prompt size: memory caps did not move the needle (~12.2k tokens/turn).**
- The bloat is restored conversation history (sessions start at messages=26) + system prompt, not MEMORY.md. Next lever: cap restored history (e.g. last 10 messages).

**Verified working this run**
- STT auto-detect (en/hi/ml), TTS language-follow, per-turn usage (STT 2.2 s/turn, TTS 152 chars/turn), Cheeko identity, tool calls.
- STT auto-reconnect survived Sarvam ASR failures in the earlier same-day session (7 recoveries).

**Action items**
1. Pin a fast provider for gemma-4-31b-it in OpenRouter (or test Google-hosted / gemini-2.5-pro); enable rate_retry_429 on EKS.
2. Cap restored history messages to cut ~half the prompt tokens.
3. EKS log persistence (CloudWatch/Fluent Bit) — a node eviction erased the first test session's logs entirely.
4. Cluster hygiene: HPA references unpublished metric picoclaw_livekit_session_load_percent; node taint evictions causing pod churn.
5. Turn detailed_trace_enabled off on EKS before real users (kid conversations in plaintext logs).
