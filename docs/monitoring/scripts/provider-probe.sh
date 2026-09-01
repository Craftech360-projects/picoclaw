#!/usr/bin/env bash
# Probes whatever LLM / STT / TTS provider the application is ACTUALLY configured to use,
# by reading the active provider rows at runtime instead of hardcoding endpoints in Kuma.
#
# Why: Kuma monitors that name a provider go stale the moment someone switches providers in
# the admin UI, and then report green against an endpoint nobody calls. That happened on
# 2026-09-01 (TTS Sarvam -> SmallestAI) within a day of the monitors being built.
#
# An unknown provider pushes DOWN rather than passing silently, so a genuinely new vendor
# still gets a human's attention - which is the part that must not be automated away.
#
# ponytail: per-vendor probes live in a case statement in shell. Ceiling: every new vendor
# needs a few lines here. If the vendor list grows past a handful, move this into the Go
# agent as a self-test endpoint instead of extending the script.
set -uo pipefail

KUMA_URL="${KUMA_URL:?set KUMA_URL}"
LLM_TOKEN="${LLM_TOKEN:?set LLM_TOKEN}"
STT_TOKEN="${STT_TOKEN:?set STT_TOKEN}"
TTS_TOKEN="${TTS_TOKEN:?set TTS_TOKEN}"
DRIFT_TOKEN="${DRIFT_TOKEN:?set DRIFT_TOKEN}"
ENVFILE=/root/xiaozhi-esp32-server/main/manager-api-node/.env
BASELINE=/var/lib/kuma-provider-baseline.txt

push() {  # push <token> <up|down> <msg>
  curl -fsS --max-time 10 -G "$KUMA_URL/api/push/$1" \
    --data-urlencode "status=$2" --data-urlencode "msg=$3" > /dev/null
}

DB=$(grep -m1 -E '^(DATABASE_URL|POSTGRES_URL)=' "$ENVFILE" | cut -d= -f2- | tr -d '\042\047')
if [ -z "$DB" ]; then
  for t in "$LLM_TOKEN" "$STT_TOKEN" "$TTS_TOKEN" "$DRIFT_TOKEN"; do
    push "$t" down "cannot read DATABASE_URL from $ENVFILE"
  done
  exit 0
fi

q() { psql "$DB" -t -A -F'|' -c "$1" 2>/dev/null; }

umask 077
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

# ---------------------------------------------------------------- LLM
row=$(q "select provider_name, model, api_base, api_key from (
          select model_name as provider_name, model, api_base, api_key, is_active from llm_providers) t
        where is_active limit 1;")
if [ -z "$row" ]; then
  push "$LLM_TOKEN" down "no active llm provider row"
else
  IFS='|' read -r P MODEL BASE KEY <<< "$row"
  # Stored as e.g. "openai/gemma4"; the leading segment is a provider-type marker the
  # agent strips before sending. Sending it verbatim returns 404 Model not found.
  SEND_MODEL=${MODEL##*/}
  case "$P" in
    sarvam-gemma4|sarvam*)
      code=$(curl -s -m 30 -o "$TMP/l" -w '%{http_code}' -X POST "$BASE/chat/completions" \
        -H "api-subscription-key: $KEY" -H 'Content-Type: application/json' \
        -d "{\"model\":\"$SEND_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":5}")
      if [ "$code" = "200" ] && grep -q '"choices"' "$TMP/l"; then
        push "$LLM_TOKEN" up "llm ok ($P/$SEND_MODEL)"
      else
        push "$LLM_TOKEN" down "llm $P http=$code $(head -c 110 "$TMP/l" | tr -d '\n')"
      fi ;;
    openrouter*|*gpt*|mistral*)
      code=$(curl -s -m 30 -o "$TMP/l" -w '%{http_code}' -X POST "$BASE/chat/completions" \
        -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
        -d "{\"model\":\"$SEND_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":5}")
      if [ "$code" = "200" ] && grep -q '"choices"' "$TMP/l"; then
        push "$LLM_TOKEN" up "llm ok ($P/$SEND_MODEL)"
      else
        push "$LLM_TOKEN" down "llm $P http=$code $(head -c 110 "$TMP/l" | tr -d '\n')"
      fi ;;
    *)
      push "$LLM_TOKEN" down "no probe implemented for LLM provider '$P' - add one to provider-probe.sh" ;;
  esac
fi

# ---------------------------------------------------------------- STT
row=$(q "select provider_name, model, api_key from stt_providers where is_active limit 1;")
if [ -z "$row" ]; then
  push "$STT_TOKEN" down "no active stt provider row"
else
  IFS='|' read -r P MODEL KEY <<< "$row"
  case "$P" in
    sarvam*)
      # 0.25s of silence. An empty transcript is the expected result; request_id proves
      # the endpoint, key and model are all live.
      python3 -c "
import wave
w=wave.open('$TMP/p.wav','wb'); w.setnchannels(1); w.setsampwidth(2); w.setframerate(16000)
w.writeframes(b'\x00\x00'*4000); w.close()" 2>/dev/null
      if [ ! -s "$TMP/p.wav" ]; then
        push "$STT_TOKEN" down "could not build probe wav"
      else
        code=$(curl -s -m 30 -o "$TMP/s" -w '%{http_code}' -X POST 'https://api.sarvam.ai/speech-to-text' \
          -H "api-subscription-key: $KEY" \
          -F "file=@$TMP/p.wav" -F "model=$MODEL" -F 'mode=transcribe' -F 'language_code=unknown')
        if [ "$code" = "200" ] && grep -q request_id "$TMP/s"; then
          push "$STT_TOKEN" up "stt ok ($P/$MODEL)"
        else
          push "$STT_TOKEN" down "stt $P http=$code $(head -c 110 "$TMP/s" | tr -d '\n')"
        fi
      fi ;;
    *)
      push "$STT_TOKEN" down "no probe implemented for STT provider '$P' - add one to provider-probe.sh" ;;
  esac
fi

# ---------------------------------------------------------------- TTS
row=$(q "select provider_name, model_id, voice_id, api_key from tts_providers where is_active limit 1;")
if [ -z "$row" ]; then
  push "$TTS_TOKEN" down "no active tts provider row"
else
  IFS='|' read -r P MODEL VOICE KEY <<< "$row"
  case "$P" in
    smallest)
      # model_id lightning_v3.1 -> URL segment lightning-v3.1
      SEG=${MODEL//_/-}
      code=$(curl -s -m 30 -o "$TMP/t" -w '%{http_code}' \
        -X POST "https://waves-api.smallest.ai/api/v1/$SEG/get_speech" \
        -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
        -d "{\"text\":\"ok\",\"voice_id\":\"$VOICE\",\"sample_rate\":24000,\"output_format\":\"pcm\",\"speed\":1}")
      # Response is raw PCM, so size is the only meaningful signal beyond the status code.
      sz=$(wc -c < "$TMP/t")
      if [ "$code" = "200" ] && [ "$sz" -gt 1000 ]; then
        push "$TTS_TOKEN" up "tts ok ($P/$MODEL/$VOICE, ${sz}B)"
      else
        push "$TTS_TOKEN" down "tts $P http=$code bytes=$sz $(head -c 90 "$TMP/t" | tr -d '\0\n')"
      fi ;;
    sarvam)
      code=$(curl -s -m 30 -o "$TMP/t" -w '%{http_code}' -X POST 'https://api.sarvam.ai/text-to-speech' \
        -H "api-subscription-key: $KEY" -H 'Content-Type: application/json' \
        -d "{\"text\":\"ok\",\"model\":\"$MODEL\",\"speaker\":\"$VOICE\",\"target_language_code\":\"hi-IN\",\"speech_sample_rate\":8000}")
      if [ "$code" = "200" ] && grep -q audios "$TMP/t"; then
        push "$TTS_TOKEN" up "tts ok ($P/$MODEL/$VOICE)"
      else
        push "$TTS_TOKEN" down "tts $P http=$code $(head -c 110 "$TMP/t" | tr -d '\n')"
      fi ;;
    elevenlabs)
      code=$(curl -s -m 30 -o "$TMP/t" -w '%{http_code}' \
        -X POST "https://api.elevenlabs.io/v1/text-to-speech/$VOICE" \
        -H "xi-api-key: $KEY" -H 'Content-Type: application/json' \
        -d "{\"text\":\"ok\",\"model_id\":\"$MODEL\"}")
      sz=$(wc -c < "$TMP/t")
      if [ "$code" = "200" ] && [ "$sz" -gt 1000 ]; then
        push "$TTS_TOKEN" up "tts ok ($P/$MODEL/$VOICE, ${sz}B)"
      else
        push "$TTS_TOKEN" down "tts $P http=$code $(head -c 110 "$TMP/t" | tr -d '\0\n')"
      fi ;;
    *)
      push "$TTS_TOKEN" down "no probe implemented for TTS provider '$P' - add one to provider-probe.sh" ;;
  esac
fi

# ---------------------------------------------------------------- drift (informational)
# The probes above now follow provider changes on their own, so this is no longer
# action-required - it is a record that the configuration moved. Excludes api_key: a key
# rotation is not a config change, and the probes above already detect a bad key.
current=$(q "select 'llm:'||model_name||'/'||model||'@'||api_base from llm_providers where is_active
             union all select 'stt:'||provider_name||'/'||model from stt_providers where is_active
             union all select 'tts:'||provider_name||'/'||model_id from tts_providers where is_active
             order by 1;" | tr -d ' ')

if [ -z "$current" ]; then
  push "$DRIFT_TOKEN" down "provider query returned nothing"
elif [ ! -f "$BASELINE" ]; then
  echo "$current" > "$BASELINE"
  push "$DRIFT_TOKEN" up "baseline recorded: $(echo "$current" | tr '\n' ' ')"
elif [ "$current" = "$(cat "$BASELINE")" ]; then
  push "$DRIFT_TOKEN" up "providers unchanged"
else
  # Baseline is NOT auto-updated. Automating that away would mean never being told a
  # provider changed at all, which is the one thing this must not do.
  push "$DRIFT_TOKEN" down "provider config changed. now: $(echo "$current" | tr '\n' ' ') - refresh $BASELINE once reviewed"
fi
