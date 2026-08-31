#!/usr/bin/env bash
# Watches Uptime Kuma from OUTSIDE Kuma and messages Telegram directly if it stops
# answering. Kuma cannot report its own death: if the instance stops, the container
# crashes, or the disk fills, every monitor simply goes quiet - and quiet is
# indistinguishable from healthy.
#
# ponytail: no new service and no new account. The dev box already has cron and the
# bot token, so it watches Kuma while Kuma watches it back (monitors 20-24, 27).
# Mutual watching does not cover both hosts dying at once; that is the accepted ceiling.
# Upgrade path if it matters: a third-party check such as healthchecks.io.
set -uo pipefail

KUMA_URL="${KUMA_URL:?set KUMA_URL}"
BOT_TOKEN="${BOT_TOKEN:?set BOT_TOKEN}"
CHAT_ID="${CHAT_ID:?set CHAT_ID}"
STATE=/var/lib/kuma-deadman.state
FAILS_BEFORE_ALERT=2      # ~10 minutes at a 5-minute cron

tg() {
  curl -fsS --max-time 15 -X POST "https://api.telegram.org/bot$BOT_TOKEN/sendMessage" \
    --data-urlencode "chat_id=$CHAT_ID" --data-urlencode "text=$1" > /dev/null
}

fails=0
[ -f "$STATE" ] && fails=$(cat "$STATE")

if curl -fsS --max-time 15 -o /dev/null "$KUMA_URL"; then
  # Recovered: say so only if we had actually alerted, to avoid chatter.
  [ "$fails" -ge "$FAILS_BEFORE_ALERT" ] && tg "[DEADMAN] Uptime Kuma is answering again at $KUMA_URL"
  echo 0 > "$STATE"
  exit 0
fi

fails=$((fails + 1))
echo "$fails" > "$STATE"

# Alert exactly once at the threshold, not on every subsequent run.
if [ "$fails" -eq "$FAILS_BEFORE_ALERT" ]; then
  tg "[DEADMAN] Uptime Kuma has not answered for $((fails * 5)) minutes at $KUMA_URL. ALL monitoring is blind until this is fixed."
fi
