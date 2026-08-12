---
status: open
assignee:
---

# 001 — Every device pairs to a child

## Parent

`docs/issues/child-owned-state/000-design.md`

## What to build

Nothing downstream works until `ai_device.kid_id` is reliably populated, and today
it usually is not. `bindDevice` writes `user_id`, `agent_id` and the board fields
and **never touches `kid_id`** — assigning a child is a separate call the parent
app's documented activation sequence does not make. That is the root cause, not
parents skipping a step.

Three changes, one slice:

**Auto-pair on bind.** If the owner has exactly one `kid_profile`, set `kid_id` to
it in the same write. If they have zero or two or more, leave it null and let the
app's existing kid picker resolve it. The survey says this alone takes pairing from
6 of 24 devices to 23 of 24.

**Delete the 409.** `assignKidByMac` and `assignKidToDevice` throw *"Device already
has a child assigned"* whenever `kid_id` is set and differs from the incoming one,
surfaced to the app as HTTP 409. That makes a wrong pairing permanent — the picker
can never correct it, and there is no unassign path in the app. Reassignment is the
expected operation, so the guard goes and repairing a pairing becomes a normal
call.

Fix the ownership asymmetry while you are in there: `assignKidByMac` only scopes by
user when a `userId` is passed, and the web route calls it without one, so any
authenticated user can pair a child to any unpaired device by MAC. The mobile route
does pass it.

**Record every pairing change.** New `device_kid_assignment` table — which child,
which MAC, when it started, when it ended — written on every pair, repair and
unpair. Nothing reads it yet. It exists because there is no record anywhere that a
toy changed hands, so once one does, that device's imagine gallery and analytics
rollups can never be split between the two children. The survey found zero devices
have changed hands so far, which is exactly why adding it now is cheap.

## Acceptance criteria

- [ ] Binding a device whose owner has exactly one kid sets `kid_id` in the same
      transaction; binding when the owner has zero or several leaves it null
- [ ] Re-pairing a device to a different child succeeds instead of returning 409,
      and the mobile picker can correct a device paired to the wrong sibling
- [ ] `assignKidByMac` refuses to pair a child to a device the caller does not own,
      from the web route as well as the mobile one
- [ ] `device_kid_assignment` gains a row on pair, on repair and on unpair, with the
      previous row's end timestamp closed out
- [ ] Unbinding still clears `ai_device.kid_id` and leaves every one of that child's
      rows untouched
- [ ] Run against DB1: paired device count moves from 6 to 23 of 24, and the
      remaining one is the device whose owner has 2+ kids
- [ ] No existing Jest assertion deleted to make a test pass

## Blocked by

None — can start immediately.
