---
status: closed
assignee: claude
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

- [x] Gateway room metadata carries `kid_id` for a paired device and `null` for an
      unpaired one — exercised directly against `buildDispatchMetadata`
- [x] Worker uses `workspace-kid-<id>` when the metadata carries a kid, and falls back
      to `workspace-device-<mac>` unchanged when it does not
- [x] A worker running the new binary against **old** gateway metadata still resolves
      the MAC directory — deploy order does not matter
- [x] Two children who have used the same toy resolve to different directories, and
      one child on two toys resolves to the same one
- [x] A directory left behind by a killed session is reused only by the same child —
      true by construction once the name is the child's
- [x] The distributed workspace lock is untouched and stays MAC-keyed
- [ ] Both binaries built and restarted on the DO dev box, one live session per repo —
      **deferred, no dev-box deploy until asked**

## Blocked by

- `docs/issues/child-owned-state/001-every-device-pairs-to-a-child.md` — the gateway
  can only put a kid id in metadata for devices that have one


## Resolution

Shipped in `b206d514` (cheeko-backend) and `21b5284` (picoclaw).

**The ticket's plan was right but its data source was wrong, and that was the whole
risk of this slice.** `child_profile` was already in the room metadata and already
carried an `id`, so the obvious implementation is to read that. It is unsafe:
`getChildProfileByMac` falls back to *the owner's most recently created child* when
the device has no pairing. Keying a workspace on it would hand an unpaired toy
whichever sibling happened to be created last — the precise bug this phase exists to
remove, arrived at by reusing a field that looked correct.

So the manager now returns `pairedKidId` next to `id`: explicitly the pairing, `null`
when there is none, with a comment saying only that field may key state. The gateway
puts it at the top of the metadata as `kid_id`. Both dispatch sites already fetch the
profile, so neither call site changed.

Two things in the Go side worth knowing. `ResolveKidID` accepts a quoted **or**
unquoted id, because a silent miss would send every session back to the MAC directory
with no error anywhere. And the value is reduced to digits before it becomes a
directory component — `"../../etc"` resolves to no kid rather than to a path, and the
test asserts that.

Verification: 8 Go subtests green, including the traversal case and the
old-gateway-metadata fallback; the gateway builder exercised for paired, unpaired and
absent-profile, confirming the unpaired case emits `null` and not the fallback id.
Manager suite 1387/1388 — the one failure is `rate-limit-logging`, which passes alone
in 3.1s and trips the pre-existing open-handle leak under parallel load.

**Not built.** `go build ./...` fails locally on `libolm` cgo, the known Windows issue
in the deploy notes, so this is `go vet` + a passing package test rather than a linked
binary. The binary gets built on the box, which is 010.
