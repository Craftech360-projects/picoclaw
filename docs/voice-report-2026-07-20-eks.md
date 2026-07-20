# Cheeko Voice Pipeline Report

Generated: 2026-07-20T07:24:33+00:00 UTC  
Log: `eks-logs.txt` (full file)

## Setup

| Stage | Provider / Model | Key config |
|---|---|---|
| STT | sarvam / saaras:v3 | language=? (auto-detect), mode=?, ? Hz |
| LLM | google/gemma-4-31b-it | max_tokens=260, temperature=0.7, streaming=true, tools=7 (via https://openrouter.ai/api/v1) |
| TTS | sarvam / bulbul:v3 | voice=pooja, 24000 Hz linear16, languages used: en-IN, hi-IN, kn-IN, ml-IN, mr-IN |
| VAD | TEN VAD | threshold=0.68, endpoint=700 ms |

## Volume

- Turns captured: **57** (55 user turns, 2 greetings)
- Sessions: 2
- Window: 2026-07-20T07:10:18+00:00 → 2026-07-20T07:22:49+00:00

## Latency (user turns, ms)

| Stage | n | avg | p50 | p95 | max |
|---|---|---|---|---|---|
| STT final transcript (speech start → final text) | 35 | 2845 | 1714 | 9012 | 11345 |
| LLM first token (after transcript) | 40 | 1414 | 753 | 3753 | 6137 |
| LLM full response | 29 | 15180 | 14995 | 23422 | 24447 |
| TTS first audio (after LLM first sentence) | 40 | 1883 | 1292 | 4289 | 8321 |
| Voice-to-voice: user stopped → first reply audio | 26 | 5434 | 4973 | 10002 | 12242 |
| Turn end-to-end total | 55 | 12538 | 15717 | 23860 | 27193 |

## Voice-to-voice by TTS language

| Language | turns | avg ms | p95 ms |
|---|---|---|---|
| en-IN | 14 | 5383 | 9372 |
| hi-IN | 3 | 7464 | 12242 |
| ml-IN | 7 | 5200 | 9983 |
| mr-IN | 2 | 3568 | 5232 |

## Usage per stage

| Stage | Billing unit | Total | Avg / turn |
|---|---|---|---|
| STT (Sarvam) | audio seconds | 0.0 s | 0.0 s |
| LLM prompt | tokens | 336985 | 11620 |
| LLM completion | tokens | 2012 | 69 |
| TTS (Sarvam) | input characters | 0 | 0 |

## Conversation log (user turns)

| # | time | lang | stt_s | llm tok in/out | tts chars | e2e ms | transcript | LLM response | TTS input |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 07:10:37 | en-IN | 0.0 | 11403/41 | 0 | 17372 |  | [happy] Yes, Rahul, I am listening to you right now! [excited] I am paying very close attention! [cu… |  |
| 2 | 07:10:41 | en-IN | 0.0 | 0/0 | 0 | 2538 |  |  |  |
| 3 | 07:10:57 | en-IN | 0.0 | 10828/70 | 0 | 16032 |  | [laughing] Arrey, Rahul! [happy] You are playing a funny game with me! [excited] I will tell you one… |  |
| 4 | 07:11:01 | en-IN | 0.0 | 0/0 | 0 | 2331 |  |  |  |
| 5 | 07:11:17 | en-IN | 0.0 | 10923/69 | 0 | 15970 |  | [silly] Oho! You are back to that question again! [happy] Like I said, I do not have a birthday with… |  |
| 6 | 07:11:22 | en-IN | 0.0 | 0/0 | 0 | 1878 |  |  |  |
| 7 | 07:11:22 | en-IN | 0.0 | 0/0 | 0 | 348 |  |  |  |
| 8 | 07:11:22 | en-IN | 0.0 | 0/0 | 0 | 270 |  |  |  |
| 9 | 07:11:41 | en-IN | 0.0 | 11089/67 | 0 | 18364 |  | [confused] Hmmm, Q plus three? [silly] That is a very tricky puzzle! [excited] If we are talking abo… |  |
| 10 | 07:11:44 | en-IN | 0.0 | 0/0 | 0 | 1955 |  |  |  |
| 11 | 07:11:44 | en-IN | 0.0 | 0/0 | 0 | 363 |  |  |  |
| 12 | 07:12:02 | kn-IN | 0.0 | 11184/72 | 0 | 17313 |  | [confused] Oho! [curious] You said two plus... and then something in Kannada! [silly] I think you sa… |  |
| 13 | 07:12:06 | kn-IN | 0.0 | 0/0 | 0 | 2842 |  |  |  |
| 14 | 07:12:33 | en-IN | 0.0 | 11278/91 | 0 | 26659 |  | [excited] Oho! Three animals coming right up! [happy] First, there is the mighty Lion with a big flu… |  |
| 15 | 07:12:38 | en-IN | 0.0 | 0/0 | 0 | 3495 |  |  |  |
| 16 | 07:12:39 | en-IN | 0.0 | 0/0 | 0 | 1167 |  |  |  |
| 17 | 07:13:04 | mr-IN | 0.0 | 11398/86 | 0 | 27193 |  | [confused] Arrey! [curious] You asked what the color is, but then you said something in Marathi! [si… |  |
| 18 | 07:13:09 | mr-IN | 0.0 | 0/0 | 0 | 1945 |  |  |  |
| 19 | 07:13:28 | en-IN | 0.0 | 10470/80 | 0 | 19574 |  | [confused] Oho! [curious] I am a little bit confused now! [silly] You are asking what color is, but … |  |
| 20 | 07:13:44 | en-IN | 0.0 | 10560/53 | 0 | 23193 |  | [excited] Oho! [happy] The sky is a beautiful, bright blue! [silly] But sometimes it turns orange an… |  |
| 21 | 07:13:49 | en-IN | 0.0 | 0/0 | 0 | 3492 |  |  |  |
| 22 | 07:14:03 | en-IN | 0.0 | 10633/65 | 0 | 14350 |  | [curious] Hmmm! [confused] Are you asking what day it is today? [happy] Today is Monday! [silly] It … |  |
| 23 | 07:14:23 | en-IN | 0.0 | 10715/70 | 0 | 18591 |  | [excited] Oho! [happy] I can do that with my eyes closed! [excited] One! [excited] Two! [excited] Th… |  |
| 24 | 07:14:29 | en-IN | 0.0 | 0/0 | 0 | 3402 |  |  |  |
| 25 | 07:14:43 | en-IN | 0.0 | 10811/65 | 0 | 13860 |  | [excited] Oho! [happy] That is a piece of cake! [silly] A dog goes woof woof! [laughing] Or sometime… |  |
| 26 | 07:14:47 | mr-IN | 0.0 | 0/0 | 0 | 2394 |  |  |  |
| 27 | 07:14:48 | mr-IN | 0.0 | 0/0 | 0 | 507 |  |  |  |
| 28 | 07:14:48 | mr-IN | 0.0 | 0/0 | 0 | 491 |  |  |  |
| 29 | 07:15:09 | hi-IN | 0.0 | 10917/83 | 0 | 20787 |  | [happy] Oho! [excited] Yes, I can understand you! [happy] You are asking me if I can understand what… |  |
| 30 | 07:15:28 | hi-IN | 0.0 | 11014/54 | 0 | 23860 |  | [happy] Oho! [excited] Mera naam Cheeko hai! [silly] I am your fun and witty best friend! [curious] … |  |
| 31 | 07:15:39 | hi-IN | 0.0 | 0/0 | 0 | 9456 |  |  |  |
| 32 | 07:15:53 | hi-IN | 0.0 | 10433/52 | 0 | 13603 |  | [excited] Arrey! [happy] Two plus two is four! [silly] I calculated it in a blink of an eye! [curiou… |  |
| 33 | 07:16:00 | en-IN | 0.0 | 0/0 | 0 | 5218 |  |  |  |
| 34 | 07:16:14 | hi-IN | 0.0 | 10506/56 | 0 | 13805 |  | [happy] Oho! [excited] That is easy peasy! [happy] Mango, Apple, and Banana! [silly] I love mangoes … |  |
| 35 | 07:16:40 | hi-IN | 0.0 | 21411/79 | 0 | 20842 |  | [happy] Oho! [excited] It is raining in Mumbai right now! [happy] The temperature is twenty seven de… |  |
| 36 | 07:16:59 | hi-IN | 0.0 | 22089/82 | 0 | 19989 |  | [happy] Oho! [excited] In Bangalore, it is partly cloudy! [happy] The temperature is twenty seven po… |  |
| 37 | 07:17:44 | ml-IN | 0.0 | 11260/65 | 0 | 20191 |  | [happy] Oho! [excited] Ente peru Cheeko ennanu! [silly] I am your fun and witty best friend! [curiou… |  |
| 38 | 07:18:33 | ml-IN | 0.0 | 11345/77 | 0 | 22871 |  | [happy] Oho! [excited] Pinne entha! [happy] I can definitely tell you a story! [excited] Let us imag… |  |
| 39 | 07:18:54 | ml-IN | 0.0 | 11443/72 | 0 | 21091 |  | [happy] Oho! [excited] Aa kathaile kathaapathrathinte peru Chintu ennayirunnu! [happy] Chintu was a … |  |
| 40 | 07:18:56 | en-IN | 0.0 | 0/0 | 0 | 6179 |  |  |  |
| 41 | 07:19:12 | ml-IN | 0.0 | 10592/77 | 0 | 16902 |  | [happy] Oho! [excited] Oru thamaasho! [laughing] Hehe, I have a super funny one for you! [silly] Why… |  |
| 42 | 07:19:18 | en-IN | 0.0 | 0/0 | 0 | 10416 |  |  |  |
| 43 | 07:19:18 | en-IN | 0.0 | 0/0 | 0 | 2319 |  |  |  |
| 44 | 07:19:34 | en-IN | 0.0 | 0/0 | 0 | 15789 |  |  |  |
| 45 | 07:19:46 | en-IN | 0.0 | 0/0 | 0 | 18763 |  |  |  |
| 46 | 07:19:59 | ml-IN | 0.0 | 0/0 | 0 | 16033 |  |  |  |
| 47 | 07:20:06 | ml-IN | 0.0 | 0/0 | 0 | 15717 |  |  |  |
| 48 | 07:20:25 | ml-IN | 0.0 | 10745/76 | 0 | 20260 |  | [happy] Oho! [excited] Enikku ettavum ishtam ulla niram blue aanu! [silly] It is the color of the bi… |  |
| 49 | 07:20:53 | en-IN | 0.0 | 10836/70 | 0 | 20389 |  | [happy] Oho! [excited] I am ready! [curious] A story about a what? [silly] A dancing dinosaur? [exci… |  |
| 50 | 07:20:57 | en-IN | 0.0 | 0/0 | 0 | 3322 |  |  |  |
| 51 | 07:21:23 | en-IN | 0.0 | 10452/96 | 0 | 25640 |  | [excited] Oho! [happy] A brave little mouse! [happy] Let us call him Monty! [excited] Monty was very… |  |
| 52 | 07:21:59 | en-IN | 0.0 | 10560/51 | 0 | 16312 |  | [confused] Oho! [curious] Wait, what is done? [silly] Did Monty get the cheese already? [happy] Or d… |  |
| 53 | 07:22:12 | en-IN | 0.0 | 10621/52 | 0 | 21039 |  | [curious] Hmmm! [silly] Are you thinking very hard? [excited] Maybe you are planning a secret missio… |  |
| 54 | 07:22:33 | en-IN | 0.0 | 10692/70 | 0 | 13110 |  | [excited] Oho! [happy] You are counting! [excited] One, two, three, four, five, six, seven, eight! [… |  |
| 55 | 07:22:49 | en-IN | 0.0 | 10777/71 | 0 | 17794 |  | [excited] Oho! [happy] You are doing it again! [excited] One, two, three, four, five, six, seven, ei… |  |

Full untruncated text per turn: `turns.csv`.
