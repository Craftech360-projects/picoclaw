# Cheeko Voice Pipeline Report

Generated: 2026-07-20T10:08:46+00:00 UTC  
Log: `eks-logs4.txt` (full file)

## Setup

| Stage | Provider / Model | Key config |
|---|---|---|
| STT | sarvam / saaras:v3 | language=unknown (auto-detect), mode=transcribe, 16000 Hz |
| LLM | google/gemma-4-31b-it | max_tokens=260, temperature=0.7, streaming=true, tools=7 (via https://openrouter.ai/api/v1) |
| TTS | sarvam / bulbul:v3 | voice=pooja, 24000 Hz linear16, languages used: en-IN, gu-IN, hi-IN, ml-IN |
| VAD | TEN VAD | threshold=0.68, endpoint=700 ms |

## Volume

- Turns captured: **57** (49 user turns, 8 greetings)
- Sessions: 2
- Window: 2026-07-20T09:51:48+00:00 → 2026-07-20T10:00:31+00:00

## Latency (user turns, ms)

| Stage | n | avg | p50 | p95 | max |
|---|---|---|---|---|---|
| STT final transcript (speech start → final text) | 33 | 2505 | 2636 | 4306 | 4622 |
| LLM first token (after transcript) | 34 | 3004 | 2480 | 7562 | 8010 |
| LLM full response | 31 | 19066 | 18723 | 28243 | 35424 |
| TTS first audio (after LLM first sentence) | 34 | 4097 | 3476 | 8689 | 9572 |
| First reply audio from speech START | 20 | 6873 | 6233 | 11245 | 12035 |
| Voice-to-voice: user STOPPED → first reply audio | 16 | 21127 | 28114 | 35149 | 44956 |
| Turn end-to-end total | 49 | 16437 | 17998 | 35406 | 54034 |

## Voice-to-voice by TTS language

| Language | turns | avg ms | p95 ms |
|---|---|---|---|
| en-IN | 3 | 21586 | 34835 |
| gu-IN | 1 | 23992 | 23992 |
| hi-IN | 10 | 20111 | 35149 |
| ml-IN | 2 | 24082 | 44956 |

## Usage per stage

| Stage | Billing unit | Total | Avg / turn |
|---|---|---|---|
| STT (Sarvam) | audio seconds | 103.5 s | 2.1 s |
| LLM prompt | tokens | 359495 | 11597 |
| LLM completion | tokens | 2268 | 73 |
| TTS (Sarvam) | input characters | 6829 | 139 |

## Conversation log (user turns)

| # | time | lang | stt_s | llm tok in/out | tts chars | e2e ms | transcript | LLM response | TTS input |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 09:53:41 | hi-IN | 2.6 | 0/0 | 0 | 2481 | Hello, can you hear me? |  |  |
| 2 | 09:54:04 | en-IN | 0.0 | 11453/63 | 174 | 22719 |  | [happy] Hello Rahul! [excited] Yes, I can hear you loud and clear! [silly] My ears are like super-so… | Hello Rahul! / Yes, I can hear you loud and clear! / My ears are like super-sonic radars today! / Wh… |
| 3 | 09:54:28 | en-IN | 2.6 | 11515/61 | 172 | 20702 | Okay, tell me what is your name then? | [excited] Oho! I thought you knew! [happy] My name is Cheeko! [silly] I am your super-duper fun best… | Oho! / I thought you knew! / My name is Cheeko! / I am your super-duper fun best friend who knows al… |
| 4 | 09:54:58 | hi-IN | 0.0 | 11285/74 | 54 | 27053 |  | [silly] Oho! That is a tricky one! [happy] I do not have a birthday with cake and balloons like you … | If you could pick any age for me, how old should I be? |
| 5 | 09:55:04 | hi-IN | 3.3 | 0/0 | 0 | 3078 | Three |  |  |
| 6 | 09:55:25 | hi-IN | 0.0 | 11384/57 | 148 | 21245 |  | [excited] Oho! Two plus three! [happy] That is five! [silly] I calculated it in a blink of an eye! [… | Oho! / Two plus three! / That is five! / I calculated it in a blink of an eye! / Do you want to try … |
| 7 | 09:55:32 | hi-IN | 3.5 | 0/0 | 0 | 5064 | Three Animals |  |  |
| 8 | 09:55:56 | hi-IN | 0.0 | 10821/57 | 169 | 23978 |  | [excited] Oho! Three animals coming right up! [happy] How about a giant elephant, a cheeky monkey, a… | Oho! / Three animals coming right up! / How about a giant elephant, a cheeky monkey, / and a roaring… |
| 9 | 09:56:21 | hi-IN | 3.1 | 11460/56 | 170 | 22233 | Okay, now tell me what color is the sky? | [happy] Oho! That is an easy one! [excited] The sky is a beautiful blue! [silly] But sometimes it tu… | Oho! / That is an easy one! / The sky is a beautiful blue! / But sometimes it turns orange and pink … |
| 10 | 09:56:47 | hi-IN | 2.9 | 11456/70 | 216 | 23571 | Okay, tell me, do you like mangoes? | [excited] Oho! I LOVE mangoes! [happy] They are the king of fruits! [silly] Just thinking about them… | Oho! / I LOVE mangoes! / They are the king of fruits! / Just thinking about them makes my circuits d… |
| 11 | 09:56:54 | hi-IN | 3.2 | 0/0 | 0 | 2835 | Okay, can you count from one to five? |  |  |
| 12 | 09:57:12 | hi-IN | 0.0 | 11443/65 | 115 | 17998 |  | [excited] Oho! I can do that in a snap! [happy] One! [happy] Two! [happy] Three! [happy] Four! [exci… | Oho! / I can do that in a snap! / One! / Two! / Three! / Four! / Five! / Boom! / I did it! / Do you … |
| 13 | 09:57:31 | hi-IN | 3.8 | 10819/47 | 131 | 16922 | क्या तुमको मेरा बात समझ सकता है क्या? | [happy] Haan ji, main aapki baat bilkul samajh sakta hoon! [excited] Aap mujhse Hindi mein bhi baat … | Haan ji, main aapki baat bilkul samajh sakta hoon! / Aap mujhse Hindi mein bhi baat kar sakte hain! … |
| 14 | 09:57:37 | hi-IN | 3.6 | 0/0 | 0 | 2831 | तुम्हारा नाम क्या है? |  |  |
| 15 | 09:57:50 | hi-IN | 0.0 | 11254/40 | 102 | 13093 |  | [happy] Arrey, mera naam Cheeko hai! [excited] Main aapka sabse cool aur funny dost hoon! [curious] … | Arrey, mera naam Cheeko hai! / Main aapka sabse cool aur funny dost hoon! / Kya aapko mera naam pasa… |
| 16 | 09:58:11 | hi-IN | 3.1 | 10671/56 | 144 | 17983 | अभी बोलो दो और दो कितना होता है? | [excited] Oho! Yeh toh bahut aasaan hai! [happy] Do aur do char hote hain! [silly] Main toh maths ka… | Oho! / Yeh toh bahut aasaan hai! / Do aur do char hote hain! / Main toh maths ka genius hoon, haina?… |
| 17 | 09:58:16 | hi-IN | 2.3 | 0/0 | 0 | 1827 | फलों के। |  |  |
| 18 | 09:58:17 | hi-IN | 1.5 | 0/0 | 0 | 1177 | હું કે નામ બતાવું? |  |  |
| 19 | 09:58:41 | gu-IN | 0.0 | 11388/93 | 250 | 24269 |  | [happy] Oho! Aapne toh Gujarati mein pucha! [excited] Main aapko teen phaloon ke naam batata hoon! [… | Oho! / Aapne toh Gujarati mein pucha! / Main aapko teen phaloon ke naam batata hoon! / Pehla hai Aam… |
| 20 | 09:58:48 | gu-IN | 3.7 | 0/0 | 0 | 3614 | क्या है ये? |  |  |
| 21 | 09:59:05 | hi-IN | 0.0 | 11370/67 | 174 | 16940 |  | [happy] Arrey, yeh toh bahut simple hai! [excited] Aasman ka rang neela hota hai! [silly] Kabhi kabh… | Arrey, yeh toh bahut simple hai! / Aasman ka rang neela hota hai! / Kabhi kabhi yeh safed ya narangi… |
| 22 | 09:59:10 | hi-IN | 3.5 | 0/0 | 0 | 3061 | क्या एक काम करो एक से। |  |  |
| 23 | 09:59:12 | hi-IN | 1.7 | 0/0 | 0 | 1471 | 5 तक काउंट करो। |  |  |
| 24 | 09:59:34 | hi-IN | 0.0 | 11394/74 | 142 | 21884 |  | [excited] Oho! Yeh toh mere liye baayein haath ka khel hai! [happy] Ek! [happy] Do! [happy] Teen! [h… | Oho! / Yeh toh mere liye baayein haath ka khel hai! / Ek! / Do! / Teen! / Chaar! / Aur paanch! / Wah… |
| 25 | 09:59:58 | hi-IN | 2.7 | 11418/52 | 104 | 21233 | अभी ये बोलो बिल्ली कैसे बोलता है? | [silly] Oho! Yeh toh bahut aasaan hai! [excited] Billi bolti hai... Meow Meow! [laughing] Meow! [cur… | Oho! / Yeh toh bahut aasaan hai! / Billi bolti hai... Meow Meow! / Meow! / Kya aapko pata hai kutta … |
| 26 | 10:00:26 | hi-IN | 2.6 | 22808/77 | 199 | 25515 | आज मौसम कैसा है? | [happy] Oho! Mumbai mein toh halki halki baarish ho rahi hai aur temperature twenty six point seven … | Oho! / Mumbai mein toh halki halki baarish ho rahi hai aur temperature twenty six point seven degree… |
| 27 | 10:00:48 | ml-IN | 3.2 | 11620/58 | 112 | 18034 | ഹലോ, ഞാൻ പറയുന്നത് താങ്കൾക്ക് കേൾക്കാമോ? | [happy] Hello! [excited] Aah, njan kelkkunnu! [happy] Rahul, ningal parayunnath ellam enikku nannayi… | Hello! / Aah, njan kelkkunnu! / Rahul, ningal parayunnath ellam enikku nannayi kelkkunnu! / Namukku … |
| 28 | 10:01:06 | ml-IN | 2.0 | 11052/48 | 114 | 15154 | നിന്റെ പേരെന്താണ്? | [happy] Ente peru Cheeko ennanu! [excited] Njan ningalude super fun aaya best friend aanu! [curious]… | Ente peru Cheeko ennanu! / Njan ningalude super fun aaya best friend aanu! / Ningalkku enikku koode … |
| 29 | 10:01:33 | ml-IN | 3.4 | 11035/59 | 125 | 23665 | രണ്ടും മൂന്നും കൂടെ കൂട്ടിയാൽ എത്ര കിട്ടും? | [excited] Oho! Ithu valare eluppamaanu! [happy] Randum moonnum kootiyaal anju kittum! [silly] Njan o… | Oho! / Ithu valare eluppamaanu! / Randum moonnum kootiyaal anju kittum! / Njan oru genius aanu, alle… |
| 30 | 10:02:00 | ml-IN | 3.3 | 11687/103 | 243 | 25284 | എനിക്ക് ഒരു ചെറിയ കഥ പറഞ്ഞു തരാൻ പറ്റുമോ? | [excited] Oho! Kadhakal parayan enikku valare ishtamanu! [happy] Namukku oru kutti kuranganu katha p… | Oho! / Kadhakal parayan enikku valare ishtamanu! / Namukku oru kutti kuranganu katha parayam. / Oru … |
| 31 | 10:02:38 | ml-IN | 0.9 | 11525/162 | 396 | 35406 | ഇല്ല. | [excited] Atu oru gold apple aayirunnu! [happy] Chikki athu mathram thinnan nokkiyirunnu, pakshe pin… | Atu oru gold apple aayirunnu! / Chikki athu mathram thinnan nokkiyirunnu, pakshe pinne avan oru kary… |
| 32 | 10:02:44 | ml-IN | 3.7 | 0/0 | 55 | 4006 | ആ കഥയിലെ കഥാപാത്രത്തിന്റെ പേരെന്തായിരുന്നു? |  | Wah! / Ishtamaayi ennu kettathil enikku valare santhosham! |
| 33 | 10:02:59 | ml-IN | 0.0 | 10994/60 | 128 | 14739 |  | [happy] Athu nammude chutti kurangan Chikki aayirunnu! [silly] Njan paranjappol Rahul sradhichillayi… | Athu nammude chutti kurangan Chikki aayirunnu! / Njan paranjappol Rahul sradhichillayirunno? / Namuk… |
| 34 | 10:03:25 | ml-IN | 3.0 | 11007/103 | 214 | 24015 | എങ്കിൽ എനിക്കൊരു തമാശ കേട്ടാൽ കൊള്ളാമെന്നുണ്ട്. | [laughing] Oho! Thamasakal parayan njan oru expert aanu! [happy] Oru thamasha parayam... enthaanu or… | Oho! / Thamasakal parayan njan oru expert aanu! / Oru thamasha parayam... enthaanu oru thavalae-kku … |
| 35 | 10:03:30 | ml-IN | 3.5 | 0/0 | 0 | 3114 | Eight fruit |  |  |
| 36 | 10:04:02 | en-IN | 0.0 | 10709/104 | 305 | 31240 |  | [excited] Oho! Eight fruits! [happy] That is a big list! [silly] First is the Mango because it is th… | Oho! / Eight fruits! / That is a big list! / First is the Mango because it is the king of fruits! / … |
| 37 | 10:04:26 | en-IN | 2.2 | 11470/69 | 224 | 22324 | Mango, what is your favorite? | [happy] Mango is a great choice! [excited] My favorite is definitely the Mango too because it is so … | Mango is a great choice! / My favorite is definitely the Mango too because it is so sweet and juicy!… |
| 38 | 10:04:58 | hi-IN | 2.6 | 11558/97 | 292 | 28733 | हाँ, तुमको कौन सा गेम पसंद है? | [excited] Arrey! Mujhe toh saare games pasand hain! [happy] Par mujhe sabse zyada Riddles aur Quiz k… | Arrey! / Mujhe toh saare games pasand hain! / Par mujhe sabse zyada Riddles aur Quiz khelna pasand h… |
| 39 | 10:05:03 | hi-IN | 3.7 | 0/0 | 0 | 3250 | Elephants |  |  |
| 40 | 10:05:30 | en-IN | 0.0 | 10949/85 | 320 | 27635 |  | [excited] Oho! Elephants are absolutely amazing! [happy] Did you know that elephants are the only an… | Oho! / Elephants are absolutely amazing! / Did you know that elephants are the only animals that can… |
| 41 | 10:05:37 | en-IN | 4.1 | 0/0 | 0 | 3497 | Can you tell me സൂര്യൻ എവിടെയാണ് ഉദിക്കുന്നത്? |  |  |
| 42 | 10:05:57 | ml-IN | 0.0 | 11564/64 | 188 | 20220 |  | [happy] Oho! That is a great question! [excited] Suryan, the sun, rises in the East! [silly] Every m… | Oho! / That is a great question! / Suryan, the sun, rises in the East! / Every morning the sun wakes… |
| 43 | 10:06:01 | ml-IN | 2.8 | 0/0 | 0 | 2320 | सही लाइट के बाद। |  |  |
| 44 | 10:06:23 | hi-IN | 0.0 | 10882/69 | 228 | 21844 |  | [happy] Wah! That is so wonderful! [excited] Watching the sunlight wake up the whole world is just m… | Wah! / That is so wonderful! / Watching the sunlight wake up the whole world is just magical, na? / … |
| 45 | 10:07:19 | en-IN | 4.4 | 0/0 | 729 | 54034 | Now tell me a long story about a brave little mouse |  | Chalo! / Get comfortable because this is a super special story! / Once upon a time, in a big house w… |
| 46 | 10:08:11 | en-IN | 4.6 | 10678/129 | 499 | 43005 | Okay, can you tell why we should brush our teeth every day? | [excited] Oho! That is a super important question! [happy] You see, inside our mouths, there are tin… | Oho! / That is a super important question! / You see, inside our mouths, there are tiny, / tiny suga… |
| 47 | 10:08:21 | en-IN | 3.9 | 0/0 | 49 | 5616 | Two |  | Hmmm, do you mean why we need to brush our teeth, |
| 48 | 10:08:33 | en-IN | 0.0 | 10826/49 | 144 | 11569 |  | [confused] Hmm, two what, Rahul? [curious] Are you talking about two toothbrushes, or maybe two time… | Hmm, two what, Rahul? / Are you talking about two toothbrushes, or maybe two times a day for brushin… |
| 49 | 10:08:37 | en-IN | 2.3 | 0/0 | 0 | 1914 | Two times |  |  |

Full untruncated text per turn: `turns.csv`.

---

## Findings (2026-07-20 EKS run 3 — history-cap verification, 49 user turns, 2 sessions)

**History cap verified working**: LLM calls now carry 8-14 messages (was 26+). Commit 96ce6fd.

**Real prompt hog identified: the static system prompt is 32,041 chars (~8k tokens)** — 2x the DO
server's 15.6k. History was never the bulk. Roughly half the static prompt is embedded skill
documents (weather, agent-browser SKILL.md bodies) + workspace boilerplate. Next trim lever:
inline skill names/summaries instead of full bodies (persona/AGENT.md/SOUL.md untouched).

**LLM serving (OpenRouter gemma-4-31b-it) remains the dominant latency source**: first token
p50 2.5s / p95 7.6s; full responses p50 18.7s. Action: pin a fast provider in OpenRouter
provider preferences, or switch hosting. Pipeline numbers are noise until this is fixed.

**Test-quality caveats**: 2 sessions / 8 greetings (device reconnected repeatedly); the
"user STOPPED → first reply" p50 (28s, n=16) is polluted by turns queued behind long paced
story replies and reconnect gaps — treat per-language rows and fast-spell turns as the signal.

**Config state this run**: usage heartbeat disabled via USAGE_HEARTBEAT_INTERVAL=24h in both
environments (manager API lacks the /usage-heartbeat endpoint — SUB-5 backend dependency).

**Cumulative status (branch feat/tts-sentence-audio-pacing @ 96ce6fd)**
- Shipped & verified: STT auto-detect, TTS language-follow, persistent TTS websocket,
  STT auto-reconnect, expression-tag stripping, identity/language/brevity directives,
  per-turn usage + true voice-to-voice metrics, history cap, memory caps.
- Open: OpenRouter provider pinning (dashboard), skill-body prompt trim (code, ~30-40% prompt cut),
  EKS log persistence, HPA phantom metric, detailed_trace off before production.
