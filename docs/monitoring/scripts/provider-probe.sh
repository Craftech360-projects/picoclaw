#!/usr/bin/env bash
# Two checks that both need database access, so they live together:
#   1. Sarvam STT reachability — Kuma cannot send multipart, so we do the upload here.
#   2. Provider config drift — the endpoint monitors in Kuma hardcode Sarvam URLs and
#      the Sarvam key, but provider selection lives in the DB and can change from an
#      admin UI. Without this, a provider switch leaves Kuma probing a provider nobody
#      uses, green, through a real outage.
set -uo pipefail

KUMA_URL="${KUMA_URL:?set KUMA_URL}"
STT_TOKEN="${STT_TOKEN:?set STT_TOKEN}"
DRIFT_TOKEN="${DRIFT_TOKEN:?set DRIFT_TOKEN}"
ENVFILE=/root/xiaozhi-esp32-server/main/manager-api-node/.env
BASELINE=/var/lib/kuma-provider-baseline.txt

push() {
  curl -fsS --max-time 10 -G "$KUMA_URL/api/push/$1" \
    --data-urlencode "status=$2" --data-urlencode "msg=$3" > /dev/null
}

DB=$(grep -m1 -E '^(DATABASE_URL|POSTGRES_URL)=' "$ENVFILE" | cut -d= -f2- | tr -d '\042\047')
if [ -z "$DB" ]; then
  push "$STT_TOKEN"   down "cannot read DATABASE_URL"
  push "$DRIFT_TOKEN" down "cannot read DATABASE_URL"
  exit 0
fi

# ---------- 1. provider config drift ----------
# Deliberately excludes api_key: a key rotation is not a config change, and the
# endpoint probes already detect a bad key.
current=$(psql "$DB" -t -A -F'|' -c "
  select 'llm:'||model_name||'/'||model||'@'||api_base from llm_providers where is_active
  union all select 'stt:'||provider_name||'/'||model from stt_providers where is_active
  union all select 'tts:'||provider_name||'/'||model_id from tts_providers where is_active
  order by 1;" 2>/dev/null | tr -d ' ')

if [ -z "$current" ]; then
  push "$DRIFT_TOKEN" down "provider query returned nothing"
elif [ ! -f "$BASELINE" ]; then
  echo "$current" > "$BASELINE"
  push "$DRIFT_TOKEN" up "baseline recorded: $(echo "$current" | tr '\n' ' ')"
elif [ "$current" = "$(cat "$BASELINE")" ]; then
  push "$DRIFT_TOKEN" up "providers unchanged"
else
  # Do NOT auto-update the baseline. A human confirms the change was intended,
  # updates the Kuma endpoint monitors, then refreshes the baseline file.
  push "$DRIFT_TOKEN" down "provider config changed - update Kuma monitors, then refresh $BASELINE. now: $(echo "$current" | tr '\n' ' ')"
fi

# ---------- 2. Sarvam STT ----------
umask 077
KEY=$(psql "$DB" -t -A -c "select api_key from stt_providers where is_active limit 1;" 2>/dev/null | tr -d '\n\r ')
MODEL=$(psql "$DB" -t -A -c "select model from stt_providers where is_active limit 1;" 2>/dev/null | tr -d '\n\r ')
if [ -z "$KEY" ]; then
  push "$STT_TOKEN" down "no active stt provider key in db"
  exit 0
fi

WAV=$(mktemp /tmp/sttprobe.XXXXXX.wav)
python3 -c "
import wave
w=wave.open('$WAV','wb'); w.setnchannels(1); w.setsampwidth(2); w.setframerate(16000)
w.writeframes(b'\x00\x00'*4000); w.close()" 2>/dev/null || { push "$STT_TOKEN" down "could not build probe wav"; rm -f "$WAV"; exit 0; }

RESP=$(mktemp)
CODE=$(curl -s -m 30 -o "$RESP" -w '%{http_code}' -X POST "https://api.sarvam.ai/speech-to-text" \
  -H "api-subscription-key: $KEY" \
  -F "file=@$WAV" -F "model=$MODEL" -F "mode=transcribe" -F "language_code=unknown")

# An empty transcript is expected for silence; request_id proves endpoint+key+model are live.
if [ "$CODE" = "200" ] && grep -q request_id "$RESP"; then
  push "$STT_TOKEN" up "stt ok ($MODEL)"
else
  push "$STT_TOKEN" down "stt http=$CODE $(head -c 120 "$RESP" | tr -d '\n')"
fi

rm -f "$WAV" "$RESP"
