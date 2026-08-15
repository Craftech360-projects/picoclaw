---
status: closed
assignee: claude
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

**Pair on bind.** The bind endpoint accepts an optional `kidId`, so the app can
name the child in the same call it already makes. When it sends none: if the owner
has exactly one `kid_profile`, pair to it; if they have zero or several, leave it
null and let the app's existing kid picker resolve it. The survey says the
sole-child fallback alone takes pairing from 6 of 24 devices to 23 of 24.

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

- [x] Bind accepts an optional `kidId` and pairs to it, on both the web and mobile
      bind routes, rejecting a child the binding user does not own
- [x] Binding a device whose owner has exactly one kid sets `kid_id` in the same
      transaction; binding when the owner has zero or several leaves it null
- [x] Re-pairing a device to a different child succeeds instead of returning 409,
      and the mobile picker can correct a device paired to the wrong sibling
- [x] `assignKidByMac` refuses to pair a child to a device the caller does not own,
      from the web route as well as the mobile one
- [x] `device_kid_assignment` gains a row on pair, on repair and on unpair, with the
      previous row's end timestamp closed out
- [x] Unbinding still clears `ai_device.kid_id` and leaves every one of that child's
      rows untouched
- [ ] Run against DB1: paired device count moves from 6 to 23 of 24, and the
      remaining one is the device whose owner has 2+ kids — **deferred, no dev-box
      deploy until asked**
- [x] No existing Jest assertion deleted to make a test pass

## Blocked by

None — can start immediately.

## Resolution

Shipped in `0b14534e` (cheeko-backend, `feat/child-owned-state`).

Scope grew by one thing during the build: bind takes an **explicit `kidId`**, so a
parent with several children gets paired at setup instead of never. The sole-child
fallback stays for the case where the app sends nothing, which is every current
release. Both bind routes accept it; the web one documents it in Swagger.

Two helpers carry all of it. `resolveKidForBinding` resolves explicit kid → sole
child → null. `recordKidAssignment` closes whichever assignment is open for a MAC
and opens a new one, no-opping when the same child is re-saved, so it is safe to
call on every write — bind, both assign paths, the admin assign, and unbind.
Everything that changes `kid_id` now goes through a transaction with it.

The 409 removal deleted a test that asserted it. Rewritten rather than deleted: the
boundary it actually protected — the incoming child must belong to the caller — is
kept as its own case, and the re-pair path now asserts success. 22 unit tests across
the two device suites, written first and seen to fail (6 red) before implementing.

`assignKidByMac`'s `userId` went from optional to required. The web route was the
only caller passing nothing, and it is fixed in the same commit; the mobile route
already passed it. Verified no other caller exists in any of the three repos.

Full suite: 1342 passed, 2 failed. Neither is this change — `imagine.test.js` fails
identically with the branch stashed, and `rate-limit-logging` passes alone (2.8s)
but trips the pre-existing open-handle leak under parallel load.

**Not verified:** the DB1 criterion. Nothing has been deployed to the dev box and
nothing will be until asked, so "6 of 24 becomes 23 of 24" is a prediction from the
survey, not a measurement. It belongs to
`docs/issues/child-owned-state/010-dev-box-promotion.md`.

Known ceiling, recorded in the commit: one open assignment per MAC is enforced in
the service rather than by a partial unique index, because expressing that index in
`schema.prisma` is not possible and a DB-only index would drift against
`prisma generate`. A concurrent double-assign writes one extra audit row that
nothing reads yet.
