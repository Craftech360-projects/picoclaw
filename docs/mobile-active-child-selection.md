# Selecting the active child in the parent app

Written for whoever changes `CheekoAI-Parent-App`. The server side is deployed;
this describes what the app should do with it and why the current behaviour is
wrong.

## The bug, from a parent's point of view

A parent activates a toy for Kishore. They open the app. The profile screen says
**Rahul, age 1**.

Nothing is wrong in the database. The toy is paired to Kishore, Kishore's history
is intact, and the pairing was recorded correctly. The app is simply displaying a
different child than the one it is talking about.

## Why it happens

`GET /toy/api/mobile/kids` is called with **no parameters**:

```
GET /toy/api/mobile/kids                                            ← no device context
GET /toy/api/mobile/homepage-recommendations?deviceId=AA%3A16…&kidId=8   ← has it
```

The app fetches every child the parent owns and then picks one:

```dart
// home_screen.dart
activeKid: loadedKids.isNotEmpty ? loadedKids.first : null,
```

`loadedKids.first` is not a choice, it is an accident. Until 2026-08-13 the
endpoint had no `ORDER BY` at all, so "first" was whatever order Postgres
returned rows in — which changes as rows are updated. A parent with several
children got an arbitrary one, and it could differ between two calls.

This got worse when child-owned state landed. Showing the wrong child used to be
a cosmetic mix-up. Now the child *is* the key to memory, quiz progress, the
workspace and the gallery, so the wrong child means the wrong history — and if
the parent edits that profile, they edit the wrong record.

## What the server does now

Two changes, both live.

**1. The list is ordered so the head is the family's active child.**

Children paired to a toy come first, ordered by their device's
`last_connected_at`; unpaired children follow, ordered by profile age. So
`loadedKids.first` now lands on the child whose toy was used most recently.

This makes today's app behave correctly without an app release. **It is a better
default, not a correct selection** — a parent with two paired toys has two right
answers and only the app knows which screen it is on.

**2. Every child now says which toy it is on.**

```jsonc
{
  "id": "15",
  "name": "Kishore",
  "device_mac": "00:16:3E:7A:11:C4",   // null when the child has no toy
  "deviceMac":  "00:16:3E:7A:11:C4",
  "is_paired": true,
  "isPaired": true,
  // …every field the app already receives, unchanged
}
```

**3. `GET /kids` accepts `?mac=`.**

```
GET /toy/api/mobile/kids?mac=00:16:3E:7A:11:C4
```

Returns an array containing **only that toy's child**. Behaviour worth knowing:

| Case | Response |
|---|---|
| Toy is paired | `[ {child} ]` |
| Toy exists, no child paired yet | `[]` |
| Toy belongs to another parent, or unknown | `404 Device not found` |
| Malformed address | `404 Device not found` |
| No `mac` parameter | Every child, active-toy first (unchanged) |

The address is normalised, so `00-16-3e-7a-11-c4` and `00:16:3E:7A:11:C4` both
work. `[]` and `404` are deliberately different: "you have no child on that toy"
and "that toy is not yours" are different answers and the app should not have to
guess which it got.

## What the app should change

**Any screen reached from a device — profile, edit profile, settings, progress —
should ask for that device's child rather than filtering a list it fetched for
another purpose.**

```dart
// Screens opened from a device: ask for that toy's child.
final kids = await apiService.getKids(mac: device.macAddress);
final child = kids.isNotEmpty ? kids.first : null;
// child == null means the toy is not paired yet -> send them to the pairing flow,
// do not fall back to another child.
```

For the home screen, where there may be no single active device, keep fetching
the whole list and take the head — that is now meaningful. But if the home screen
has a selected device, prefer the explicit form.

**Do not** re-derive the child by matching `kidId` from the devices list against
a separately fetched kids list. That works today and is one refactor away from
drifting, which is the same shape as the bug being fixed here: two sources for
one fact.

### The null case matters

`[]` from `?mac=` means the toy has no child yet. Today the app would fall back
to `loadedKids.first` and cheerfully show a sibling. That must become an explicit
"this toy isn't set up yet" path, because the alternative is showing one child's
memory under another child's name.

## Related: stop creating a duplicate child on every activation

The same activation flow used to call `POST /kids` every time it ran, minting a
new profile identical on name and birth date. On the dev database that produced
two Kishores and two Rahuls; one parent's real child had 67 memory documents and
71 voice sessions stranded under the older profile while the toy pointed at the
new, empty one.

The server now de-duplicates: `POST /kids` returns the existing profile when
`(user_id, lower(name), birth_date)` already matches, so a repeat activation is
harmless. A child with no birth date is always created — one field is not enough
to match on.

The app should still avoid asking. If the parent already has children, offer them
as a choice before offering "add a new child". Creating is the exception, not the
default step in the wizard.

## Verifying a change

Against the dev box (`64.227.170.31:8002`), with a real Firebase token:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://64.227.170.31:8002/toy/api/mobile/kids?mac=00:16:3E:7A:11:C4"
```

Expect exactly one child, and expect it to be the one the toy is paired to. Then
open the profile screen in the app for that toy and confirm the same name.
