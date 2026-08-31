# Sarvam API endpoints used by the agent (curl)

Auth for everything: header `api-subscription-key: $SARVAM_API_KEY`.

## STT — REST (what the agent uses for Manual Talk / PTT)

Source: `pkg/voice/stt/sarvam_rest_provider.go` — `POST https://api.sarvam.ai/speech-to-text`, multipart WAV, model `saaras:v4`, `mode=transcribe`, `language_code=unknown` for auto-detect. Sync endpoint rejects clips > 30s.

```bash
curl -X POST "https://api.sarvam.ai/speech-to-text" \
  -H "api-subscription-key: $SARVAM_API_KEY" \
  -F "file=@utterance.wav" \
  -F "model=saaras:v4" \
  -F "mode=transcribe" \
  -F "language_code=unknown"
```

Response: `{"transcript": "...", "language_code": "hi-IN", "request_id": "..."}`

## TTS — what the agent actually uses is the websocket, not REST

Source: `pkg/voice/sarvam_tts/tts.go` — `wss://api.sarvam.ai/text-to-speech/ws?model=bulbul:v3&send_completion_event=true`, speaker `pooja`, output `linear16` PCM at 24000 Hz. Not reachable with plain curl (websocket).

## TTS — REST equivalent (one-shot synthesis)

```bash
curl -X POST "https://api.sarvam.ai/text-to-speech" \
  -H "api-subscription-key: $SARVAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "text": "नमस्ते, मैं चीको हूँ!",
    "model": "bulbul:v3",
    "speaker": "pooja",
    "target_language_code": "hi-IN",
    "speech_sample_rate": 24000
  }'
```

Response: `{"audios": ["<base64 wav>"], "request_id": "..."}` — decode the first `audios` element to get the WAV.

Decode to a file:

```bash
curl -s -X POST "https://api.sarvam.ai/text-to-speech" \
  -H "api-subscription-key: $SARVAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"text":"Hello!","model":"bulbul:v3","speaker":"pooja","target_language_code":"hi-IN"}' \
  | python -c "import sys,json,base64;open('out.wav','wb').write(base64.b64decode(json.load(sys.stdin)['audios'][0]))"
```
