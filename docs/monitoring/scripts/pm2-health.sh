#!/usr/bin/env bash
# Reports pm2 restart-rate health to an Uptime Kuma push monitor.
#
# ponytail: alerting logic lives in shell because these droplets have no Prometheus.
# Ceiling: no history, no rate-of-change queries, no versioned rules. If the DO boxes
# become long-lived production, replace this with Prometheus, do not extend this script.
set -uo pipefail

KUMA_URL="${KUMA_URL:?set KUMA_URL}"
PUSH_TOKEN="${PUSH_TOKEN:?set PUSH_TOKEN}"
STATE=/var/lib/kuma-pm2-state.json

# Services on the voice path. line-art / visitors-register / admin-dashboard are not.
WATCH="manager-api manager-web picoclaw-livekit gw-0 gw-1 gw-2 gw-3"

# Restarts per 5-minute window that count as crash-looping rather than a one-off.
#
# pm2's restart_time is a CUMULATIVE lifetime counter, not a rate. Reading it next to
# pm2's "uptime" column (time since the LAST restart, not a measurement window) is how
# 699 lifetime restarts got misread as 699-in-3-hours during this work. Measured
# 2026-08-31: manager-api 699 lifetime restarts but 363 minutes of continuous uptime;
# picoclaw-livekit 129 lifetime but 193 minutes up. Both stable.
#
# This script therefore compares snapshots and alerts on the DELTA, which is the only
# meaningful reading of a cumulative counter.
THRESHOLD="${THRESHOLD:-3}"

push() {
  curl -fsS --max-time 10 -G "$KUMA_URL/api/push/$PUSH_TOKEN" \
    --data-urlencode "status=$1" --data-urlencode "msg=$2" > /dev/null
}

now=$(pm2 jlist 2>/dev/null) || { push down "pm2 jlist failed"; exit 0; }

prev='{}'
[ -f "$STATE" ] && prev=$(cat "$STATE")

problems=""
snapshot='{}'

for svc in $WATCH; do
  line=$(echo "$now" | jq -r --arg n "$svc" '.[] | select(.name==$n) | "\(.pm2_env.status) \(.pm2_env.restart_time)"')
  if [ -z "$line" ]; then
    problems="$problems $svc:MISSING"
    continue
  fi
  status=${line% *}
  restarts=${line#* }
  snapshot=$(echo "$snapshot" | jq --arg n "$svc" --argjson r "$restarts" '.[$n]=$r')

  if [ "$status" != "online" ]; then
    problems="$problems $svc:$status"
    continue
  fi

  was=$(echo "$prev" | jq -r --arg n "$svc" '.[$n] // empty')
  if [ -n "$was" ]; then
    delta=$((restarts - was))
    # A pm2 resurrect resets the counter; a negative delta is not a fault.
    [ "$delta" -gt "$THRESHOLD" ] && problems="$problems $svc:${delta}restarts"
  fi
done

echo "$snapshot" > "$STATE"

if [ -n "$problems" ]; then
  push down "unhealthy:$problems"
else
  push up "all watched services healthy"
fi
