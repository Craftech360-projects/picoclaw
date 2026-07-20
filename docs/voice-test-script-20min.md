# Cheeko 20-Minute Voice Test Script

**Rules**
- Speak each line clearly, then **wait for the full reply + 2 seconds** before the next line (except Phase 7, which is deliberate interruption).
- Quiet room, one speaker, ~30–50cm from the device.
- If a reply comes in the wrong language, just continue — that mismatch is data we want.
- Timings are approximate; move to the next phase when the clock says so.

---

## Phase 1 — English quick Q&A (0:00–3:00) · baseline latency
1. "Hello! Can you hear me?"
2. "What is your name?"
3. "How old are you?"
4. "What is two plus three?"
5. "Name three animals."
6. "What color is the sky?"
7. "Do you like mangoes?"
8. "What day is it today?"
9. "Can you count from one to five?"
10. "What sound does a dog make?"

## Phase 2 — Hindi quick Q&A (3:00–6:00)
1. "नमस्ते! क्या तुम मुझे सुन सकते हो?" *(namaste! kya tum mujhe sun sakte ho?)*
2. "तुम्हारा नाम क्या है?" *(tumhara naam kya hai?)*
3. "दो और दो कितने होते हैं?" *(do aur do kitne hote hain?)*
4. "मुझे तीन फलों के नाम बताओ।" *(mujhe teen phalon ke naam batao)*
5. "आसमान का रंग क्या है?" *(aasmaan ka rang kya hai?)*
6. "एक से पाँच तक गिनती करो।" *(ek se paanch tak ginti karo)*
7. "बिल्ली कैसे बोलती है?" *(billi kaise bolti hai?)*
8. "आज मौसम कैसा है?" *(aaj mausam kaisa hai?)*

## Phase 3 — Malayalam Q&A + story (6:00–9:30)
1. "ഹലോ, എന്നെ കേൾക്കാമോ?" *(hallo, enne kelkkaamo?)*
2. "നിന്റെ പേര് എന്താണ്?" *(ninte peru enthaanu?)*
3. "രണ്ടും മൂന്നും കൂട്ടിയാൽ എത്ര?" *(randum moonnum koottiyaal ethra?)*
4. "എനിക്ക് ഒരു ചെറിയ കഥ പറഞ്ഞു തരാമോ?" *(enikku oru cheriya katha paranju tharaamo?)* — **let the full story play**
5. "ആ കഥയിലെ കഥാപാത്രത്തിന്റെ പേര് എന്തായിരുന്നു?" *(aa kathayile kathaapaathrathinte peru enthaayirunnu?)*
6. "എനിക്ക് ഒരു തമാശ പറയാമോ?" *(enikku oru thamaasha parayaamo?)*

## Phase 4 — Kannada Q&A (9:30–12:00)
1. "ಹಲೋ, ನನ್ನ ಮಾತು ಕೇಳಿಸುತ್ತಿದೆಯಾ?" *(halo, nanna maatu kelisuttideyaa?)*
2. "ನಿನ್ನ ಹೆಸರೇನು?" *(ninna hesarenu?)*
3. "ಎರಡು ಮತ್ತು ಮೂರು ಕೂಡಿದರೆ ಎಷ್ಟು?" *(eradu mattu mooru koodidare eshtu?)*
4. "ಮೂರು ಪ್ರಾಣಿಗಳ ಹೆಸರು ಹೇಳು." *(mooru praanigala hesaru helu)*
5. "ಒಂದು ಚಿಕ್ಕ ಕಥೆ ಹೇಳು." *(ondu chikka kathe helu)* — **let it finish**
6. "ಒಂದು ತಮಾಷೆ ಹೇಳು." *(ondu tamaashe helu)*

## Phase 5 — Language switching rapid-fire (12:00–14:00)
*(one line each, alternating — tests auto-detect + TTS voice switching)*
1. English: "What is your favorite fruit?"
2. Malayalam: "നിനക്ക് ഏറ്റവും ഇഷ്ടമുള്ള നിറം ഏതാണ്?" *(ninakku ettavum ishtamulla niram ethaanu?)*
3. Hindi: "तुम्हें कौन सा खेल पसंद है?" *(tumhein kaun sa khel pasand hai?)*
4. Kannada: "ನಿನಗೆ ಯಾವ ಹಾಡು ಇಷ್ಟ?" *(ninage yaava haadu ishta?)*
5. English: "Tell me something about elephants."
6. Malayalam: "സൂര്യൻ എവിടെ ഉദിക്കുന്നു?" *(sooryan evide udikkunnu?)*

## Phase 6 — Long-form replies (14:00–17:00)
1. "Tell me a long story about a brave little mouse." — **let it fully finish**
2. "എനിക്ക് ഒരു വലിയ കഥ പറഞ്ഞു തരൂ, ഒരു ആനയെ കുറിച്ച്." *(a long story about an elephant — let it finish)*
3. "Explain why we should brush our teeth every day."

## Phase 7 — Barge-in test (17:00–19:00)
*(deliberately interrupt — tests interruption + recovery)*
1. "Tell me a very long story about a dragon." → **after ~5 seconds of reply, interrupt loudly with:** "Stop! Tell me a joke instead."
2. "Count from one to fifty." → **after ~5 seconds, interrupt:** "Okay okay, stop counting!"
3. "മറ്റൊരു നീണ്ട കഥ പറയൂ." *(another long story)* → **after ~5 seconds, interrupt:** "വേണ്ട, ഒരു പാട്ട് മതി." *(venda, oru paattu mathi)*

## Phase 8 — Wind down (19:00–20:00)
1. "What did we talk about today?"
2. "Thank you, you did a good job!"
3. "Goodbye, see you tomorrow!"

---

**After the test**: tell Claude "check now" — the report will be generated from this window.
