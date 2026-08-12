---
status: open
assignee:
---

# 005 — The worker's workspace directory is per child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

The only slice that leaves the manager API. Spans `mqtt-gateway` (one field) and
`picoclaw` (one case).

The worker names its workspace directory `workspace-device-<mac>`. That directory is
deleted at session end, so in the normal case it re-hydrates from the manager and
picks up whatever 003 keyed it to. But teardown does not always run — a crash, an OOM
kill, a preempted handoff — and then the directory survives. The next session on that
MAC, now a different child, reuses it. Hydration overwrites the files it knows about;
files the previous child had and the new one does not (`memory/state/` quiz ledgers,
story ledgers) simply linger.

Name the directory by child instead. `resolveLiveKitWorkspaceLifecycle` resolves
identity from room metadata and the room name **before any manager call**, so the
kid id has to arrive in the dispatch metadata:

- **Gateway**: include `kid_id` in the LiveKit room metadata it builds at
  `_deferredSetup`. It already queries the manager there for mode, character and the
  child profile, so this is one field on a call that already happens.
- **Worker**: a new case above the existing MAC case —
  `workspace-kid-<id>` when the metadata carries a kid, `workspace-device-<mac>` when
  it does not. The MAC branch stays exactly as it is for unpaired devices.

This also disposes of the crash-leftover problem rather than mitigating it: a stale
`workspace-kid-7` directory belongs to kid 7 either way, so there is nothing left to
leak across children.

Metadata parsing must tolerate the field being absent — the gateway and the worker
deploy separately, and a worker that has shipped first will see metadata without it.

## Acceptance criteria

- [ ] Gateway room metadata carries `kid_id` for a paired device and omits it (or
      sends null) for an unpaired one
- [ ] Worker uses `workspace-kid-<id>` when the metadata carries a kid, and falls back
      to `workspace-device-<mac>` unchanged when it does not
- [ ] A worker running the new binary against **old** gateway metadata still starts a
      session and still uses the MAC directory — deploy order does not matter
- [ ] Two children who have used the same toy get different directories, verified on
      the box, not inferred
- [ ] A directory left behind by a killed session is reused only by the same child
- [ ] The distributed workspace lock still serialises correctly across a session
      handoff — it stays MAC-keyed, and one child has one device at a time
- [ ] Both binaries built and restarted on the DO dev box, and one live session run
      per repo change

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md` — the gateway
  can only put a kid id in metadata for devices that have one
