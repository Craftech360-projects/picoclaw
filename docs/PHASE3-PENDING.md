# Phase 3 — Pending / Deferred (Worker)

Phase 3 is committed: the worker is persona-agnostic and PULLs persona by characterId
(`GET /character/:id/session`) every session (ADR-0003). AGENT.md = persona-agnostic
scaffold + Manager `systemPrompt`; SOUL.md = Manager `soul`; both regenerated each
session. USER.md is unchanged (write-once + restore). Manager-down keeps the last-rendered
AGENT.md/SOUL.md as a degraded fallback. `prompts/cheeko.tmpl` + its render block removed.

## 1. Per-character language → TTS/STT — NOT wired (deferred)

`language` now flows end-to-end (gateway metadata → `roomMetadata.Language` →
`managerCharacterSession.Language`), and the persona pull stores it on
`bootstrap.Metadata.Language`. But `sessionLanguagePolicy` (main.go, ~line 559) was
**deliberately left unchanged** to avoid disturbing the existing card/session-language
selection. Today every seeded character is English (locked decision #5), so this is a no-op.

- **To close:** after the existing policy block, prefer `bootstrap.Metadata.Language`
  when no explicit session/card language is set:
  ```go
  if SessionLanguageName == "" && SessionLanguageCode == "" {
      if lang := strings.TrimSpace(bootstrap.Metadata.Language); lang != "" {
          sessionLanguagePolicy = livekit.NormalizeSessionLanguagePolicy(lang, lang)
      }
  }
  ```
  Keep card/session language highest priority, character language next, primary last.

## 2. Local test execution blocked by missing native VAD lib — VERIFY ELSEWHERE

`go build ./cmd/picoclaw-livekit/` compiles cleanly (reaches the linker). The unit tests
could NOT be executed on this machine: the package requires the ten-vad cgo library, and
`third_party/ten-vad/lib/` ships only `Windows/x86` (+ Android/Web) — no Windows **x64**
static lib — so the cgo test binary can't link here.

- **Status:** source compiles; `gofmt -e` clean (repo files are CRLF, so `gofmt -l` flags
  line-endings only — do not `gofmt -w`). New tests added in
  `cmd/picoclaw-livekit/character_session_phase3_test.go`:
  metadata `character_id`/`language` parse, `fetchManagerCharacterSession`,
  `injectPersona`, persona regeneration, and degraded-fallback.
- **To close:** run `go test ./cmd/picoclaw-livekit/` in CI/Linux (or a Windows x64 env
  with the ten-vad x64 static lib present).

## 3. Live device-switch verification (handoff P3) — NOT run

Handoff P3 asks: on one device switch Cheeko → Cheeko German → (later) Math Tutor and
confirm AGENT.md + SOUL.md swap, no "I am Cheeko" bleed, all PicoClaw feature sections
intact, and per-child USER.md correct while AGENT.md identical across children.

- **Why pending:** needs a running device + Manager + LiveKit; not exercisable here.
  Also gated on seeding the Cheeko persona (systemPrompt + soul) into the DB first
  (rollout step between P2 and P3).

## Resolved open question

- **`workspace-template/SOUL.md` role:** KEPT as a generic, persona-agnostic degraded
  placeholder (not deleted). It is only used when the Manager soul is unavailable on a
  fresh workspace; normal sessions overwrite SOUL.md with the Manager `soul`.
