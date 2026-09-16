# Deploying the realtime vendors (Gemini Live, Grok Voice) to the dev box

Branch: picoclaw `feat/realtime-vendors` (30 commits from `feat/gptlive-elastic-playout`),
cheeko-backend `feat/python-agent-picoclaw-parity` (3 commits: manager, manager-web, dashboard).
Dev box: `root@64.227.170.31`. Companion doc: `deploy/dev/README.md`.

After this, one worker named `cheeko-agent` serves every character on the box, so a real
test device and the dashboard's GPT-Live tab both reach it, on whichever vendor the
manager's active Realtime row selects.

## What changes on the box

- Realtime API keys come **only** from the manager's `realtime_providers` rows.
  `OPENAI_API_KEY` / `GOOGLE_API_KEY` / `XAI_API_KEY` in the environment are ignored by
  the realtime pipeline. A row with no key means a failed session, not a fallback to env.
- A Gemini or Grok row with a blank key falls back to the GPT-Live row's key from the DB.
- The cascade worker (`picoclaw-livekit`) must be stopped: two workers cannot hold
  `cheeko-agent` at once. While this runs there is no cascade fallback.

## Before deploying (local)

1. GPT-Live regression session on the local agent (plan Task 8 Step 6).
2. One Grok session confirming non-zero tokens after commit `010d8ff4`.
3. Review of `010d8ff4`, then the final whole-branch review, and fix what it finds.

## Step 1 — cheeko-backend (manager, manager-web, dashboard)

The dev box runs manager-api under pm2 and **applies every unapplied migration on
restart**. Check `prisma/migrations` for anything unexpected before restarting.

1. Push `feat/python-agent-picoclaw-parity` and merge to `main`.
   A push to `main` triggers the GitHub "Deploy" workflow — expected here, but it is the
   irreversible step: confirm before doing it.
2. On the box, confirm `20260915000000_realtime_vendors_voices` applied and
   `npx prisma generate` ran (the new `vendor` column and the per-vendor voice columns
   are selected by name; without generate they throw at runtime).
3. Verify: `GET /livekit/providers` returns three realtime rows with `vendor`
   `openai`, `xai`, `google`. Report keys as present/absent, never their values.

## Step 2 — keys and models in the dev manager

In manager-web against dev, Runtime Providers → Realtime:

1. `openai-gpt-live`: paste the GPT-Live key. **Without it every session fails**, since
   the env fallback is gone.
2. `google-gemini-live`: paste the Gemini key; set Voice Model to
   `gemini-3.1-flash-live-preview` (2x faster replies than 2.5 in local testing, at
   roughly 2x the prompt tokens per turn).
3. `xai-grok-voice`: paste the xAI key.
4. Leave exactly one row active. Activating Gemini or Grok makes it the default for
   every session on the box; the dashboard can still pick a provider per session.
5. On **all three** rows, check `model`, `backend_model`, `api_base` and `voice`, not
   just the key: GPT-Live now inherits these from the row instead of hard-coding its
   defaults, so a stale value in `openai-gpt-live` silently changes the model, the
   backend model or the voice. Blank fields keep the built-in defaults; `api_base`
   must be `ws://`/`wss://` or it is ignored.

## Step 3 — picoclaw worker

1. Push `feat/realtime-vendors` to origin (nothing has been pushed so far).
2. On the box:

```bash
cd /root/picoclaw && git fetch origin feat/realtime-vendors && git checkout feat/realtime-vendors
export PATH=$PATH:/usr/local/go/bin
export CGO_LDFLAGS='-lc++ -lc++abi'
make build-livekit
pm2 stop picoclaw-livekit
PICOCLAW_LIVEKIT_PIPELINE=gptlive \
  pm2 start build/picoclaw-livekit --name picoclaw-gptlive --time -- \
  --agent-name cheeko-agent --config /root/.picoclaw/config.json --log-level info
pm2 save
```

No vendor API keys in that command: the keys come from the manager rows.

## Step 4 — verify on the box

`pm2 logs picoclaw-gptlive --lines 60 --nostream` should show:

- `Worker registered agent=cheeko-agent`
- per session: `gptlive: session spec selected ... vendor=… model=… voice=… in_rate_hz=…`
  (`in_rate_hz=16000` for Gemini, `24000` for GPT-Live and Grok)
- `gptlive: greeting sent greet_source=track_subscribed` within ~1.5 s of the join
- `gptlive: tool call name=quiz_score_answer` on a scored quiz answer
- `Post-session usage summary persisted` at the end
- no `session error` that is not recoverable, and no `vendor socket ended` with a
  close code while the session is live
- `tool result ... response_sent=false`: the tool result did not reach the model. On
  Gemini it is now queued and replayed on the resumed socket — the follow-up line is
  `tool result delivered on the resumed socket`. A `tool result dropped` line instead
  means the model never got it, so a quiz can stall mid-question.
- `gemini live: usage` at most once per turn (token totals are per turn)

Run one session from a real test device and one from the dashboard's GPT-Live tab, on
each vendor you enabled.

## Rollback

```bash
pm2 stop picoclaw-gptlive && pm2 start picoclaw-livekit && pm2 save
```

That restores the cascade for every character. The manager changes are additive and
need no rollback; to stop vendor selection, point the active row back at
`openai-gpt-live`.

## Risks

- **No key, no session.** The DB-only rule makes an empty key a hard failure. Check all
  three rows before the first session.
- **One agent name.** `cheeko-agent` on GPT-Live means every character on the box is on
  the realtime pipeline with no cascade fallback.
- **Migrations on restart.** The manager applies all unapplied migrations at once.
- **Grok billing.** xAI's token counts arrive in a shape their own docs do not match;
  `010d8ff4` reads the one they actually send, but until a live run confirms non-zero
  tokens, treat `billable_audio_seconds` as the reliable quantity.
- **Gemini 3.1 is preview.** Tool calls block its speech; local quiz tools return in
  under a millisecond, but a slow manager call would be heard as dead air.
