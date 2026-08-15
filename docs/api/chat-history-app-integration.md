# Parent app — models and the changes needed

> Companion to `chat-history-api-changes.md`, which describes the server side.
> Target: `Cheeko-mobile_app/CheekoAI-Parent-App` (Flutter).
>
> Two separate pieces of work: **A. the new per-child history screens**, and
> **B. three fixes to flows the app already has.** B is small and worth doing
> first — one of its bugs is what filled the database with duplicate characters.

---

## Request / response models

The envelope is `{ code, msg, data }` with **`code == 0` meaning success**. The
app's existing services already unwrap this; these models describe `data` only.

### `KidCharacter` — `GET /api/mobile/kids/{kidId}/characters` → `List<KidCharacter>`

```dart
class KidCharacter {
  final String agentId;        // pass straight to the sessions call
  final String? agentName;     // null if the agent row was deleted
  final int sessionCount;      // summed across duplicate rows of this character
  final DateTime? lastSessionAt;

  KidCharacter({
    required this.agentId,
    this.agentName,
    required this.sessionCount,
    this.lastSessionAt,
  });

  factory KidCharacter.fromJson(Map<String, dynamic> json) => KidCharacter(
    agentId: json['agentId'] as String,
    agentName: json['agentName'] as String?,
    sessionCount: (json['sessionCount'] as num?)?.toInt() ?? 0,
    lastSessionAt: json['lastSessionAt'] == null
        ? null
        : DateTime.parse(json['lastSessionAt'] as String).toLocal(),
  );
}
```

Already sorted newest-first by the server; do not re-sort unless you want a
different order.

### `KidSession` / `KidSessionPage`

`GET /api/mobile/kids/{kidId}/characters/{agentId}/sessions?page=1&limit=20`
(`limit` is capped at 100 server-side).

```dart
class KidSession {
  final String sessionId;
  final DateTime? startedAt;
  final DateTime? endedAt;     // null if the session never closed cleanly
  final int messageCount;
  final String? summary;       // null until the worker writes one at session end

  factory KidSession.fromJson(Map<String, dynamic> json) => KidSession(
    sessionId: json['sessionId'] as String,
    startedAt: json['startedAt'] == null ? null : DateTime.parse(json['startedAt']).toLocal(),
    endedAt:   json['endedAt']   == null ? null : DateTime.parse(json['endedAt']).toLocal(),
    messageCount: (json['messageCount'] as num?)?.toInt() ?? 0,
    summary: json['summary'] as String?,
  );

  KidSession({required this.sessionId, this.startedAt, this.endedAt,
              required this.messageCount, this.summary});
}

class KidSessionPage {
  final int total;                 // across all pages, for the page count
  final List<KidSession> list;

  factory KidSessionPage.fromJson(Map<String, dynamic> json) => KidSessionPage(
    total: (json['total'] as num?)?.toInt() ?? 0,
    list: ((json['list'] as List?) ?? [])
        .map((e) => KidSession.fromJson(e as Map<String, dynamic>))
        .toList(),
  );

  KidSessionPage({required this.total, required this.list});
}
```

### `ChatMessage` / `SessionMessagePage`

`GET /api/mobile/kids/{kidId}/sessions/{sessionId}/messages?cursor=0&limit=100`
(`limit` capped at 500).

**Cursor, not page.** The cursor is the message `sequence` and is *exclusive*:
send back the `nextCursor` you were handed and you get the messages after it.

```dart
class ChatMessage {
  final int sequence;          // also the pagination cursor
  final String role;           // 'user' | 'assistant'
  final String content;        // never null; may be ''
  final DateTime? createdAt;

  bool get isChild => role == 'user';

  factory ChatMessage.fromJson(Map<String, dynamic> json) => ChatMessage(
    sequence: (json['sequence'] as num).toInt(),
    role: json['role'] as String,
    content: (json['content'] as String?) ?? '',
    createdAt: json['createdAt'] == null ? null : DateTime.parse(json['createdAt']).toLocal(),
  );

  ChatMessage({required this.sequence, required this.role,
               required this.content, this.createdAt});
}

class SessionMessagePage {
  final String sessionId;
  final List<ChatMessage> messages;   // ascending by sequence
  final bool hasMore;
  final int? nextCursor;              // null when hasMore is false

  factory SessionMessagePage.fromJson(Map<String, dynamic> json) => SessionMessagePage(
    sessionId: json['sessionId'] as String,
    messages: ((json['messages'] as List?) ?? [])
        .map((e) => ChatMessage.fromJson(e as Map<String, dynamic>))
        .toList(),
    hasMore: json['hasMore'] == true,
    nextCursor: (json['nextCursor'] as num?)?.toInt(),
  );

  SessionMessagePage({required this.sessionId, required this.messages,
                      required this.hasMore, this.nextCursor});
}
```

### Service methods to add

Alongside the existing calls in `lib/services/java_api_service.dart`, using the
same Firebase-token headers those already build:

```dart
Future<List<KidCharacter>> getKidCharacters(String kidId);
Future<KidSessionPage>     getKidCharacterSessions(String kidId, String agentId, {int page = 1, int limit = 20});
Future<SessionMessagePage> getKidSessionMessages(String kidId, String sessionId, {int cursor = 0, int limit = 100});
```

Paths:

```
GET {base}/toy/api/mobile/kids/{kidId}/characters
GET {base}/toy/api/mobile/kids/{kidId}/characters/{agentId}/sessions?page=&limit=
GET {base}/toy/api/mobile/kids/{kidId}/sessions/{sessionId}/messages?cursor=&limit=
```

Errors to handle: **404** (kid not yours, or no such kid), **400** (malformed
`kidId`), **401** (token expired — refresh and retry).

---

## A. The history screens

Three levels, one per endpoint:

1. **Characters** — for the selected child. Rows: name, `sessionCount`,
   `lastSessionAt`. Empty list is normal and means "this child has not talked to
   anything yet" — it is not an error.
2. **Sessions** — for the tapped character. Page as the user scrolls, using
   `total` to know when to stop. Show `summary` as the row subtitle; fall back to
   the date when it is `null`.
3. **Transcript** — for the tapped session. Render `role == 'user'` as the child
   and `assistant` as the character. Page with `nextCursor` until `hasMore` is
   false.

**Which `kidId`?** These screens are per child, so the app must have an active
child selected. That selection is described in
`docs/mobile-active-child-selection.md`. Do not derive the child from the toy —
that is the coupling this whole change removes.

### Replace the old calls

`java_api_service.dart` currently uses:

```
:1137   {base}/toy/api/mobile/agents/{agentId}/sessions
:1185   {base}/toy/api/mobile/agents/{agentId}/chat-history/{sessionId}
```

Those are **account-wide**: a character, whoever spoke to it. On an account with
two children they mix both children's conversations together. They still work and
their shape has not changed, so the swap can be done screen by screen — but any
screen presented as "this child's history" must move to the kid-scoped endpoints
to be correct.

---

## B. Three fixes to existing flows

### B1. Toy activation must stop creating an agent per attempt

`lib/controllers/toy_activation_controller.dart:582-599`:

```dart
final existingAgents = await _agentService!.getUserAgents();
final agentName = _generateUniqueAgentName(existingAgents);   // "Cheeko", "Cheeko 2", …
newAgentId = await _agentService!.createAgent(agentName: agentName);
final bindResult = await _agentService!.bindDevice(agentId: newAgentId, deviceCode: _deviceCode);
```

Two things go wrong. `_generateUniqueAgentName` (`:809`) invents `Cheeko 2`, but
the server normalises the suffix away and stores `Cheeko` — so the next activation
reads back three agents all named `Cheeko`, the counter produces `Cheeko 2` again,
and the account grows another row. And when `bindDevice` fails, the agent created
one line earlier stays behind: a mistyped code retried twice left **three** rows
in 44 seconds.

The server now returns the existing row instead of creating a second, so the
damage is stopped without an app release. The app should still be cleaned up:

- Drop `_generateUniqueAgentName`. Pass the plain character name (`'Cheeko'`).
- Treat `createAgent`'s result as **create-or-get** — the id may be one the app has
  seen before. The existing `boundAgentId != newAgentId` verification is still
  valid and should stay.
- On a failed bind, **retry only the bind**, not the create-then-bind pair.
- Verify the cleanup in the catch block actually deletes the agent it created; the
  comment says it does, and two orphaned rows say otherwise.

### B2. Bind errors are now 4xx, so the messages can be real

Every bind failure used to be a `500`. Now:

| status | meaning | suggested message |
|---|---|---|
| 400 | wrong or expired 6-digit code | "That code didn't work. Check it and try again." |
| 404 | no such device, or agent not yours | "We couldn't find that toy." |
| 409 | toy already bound to another account | "This toy is already set up on another account." |

`DeviceBindingErrorType` can now be driven by status code instead of guessing from
a 500.

### B3. One agent may have several toys

Because two toys running Cheeko now share one agent row,
`GET /api/mobile/agents/{agentId}/devices` can return more than one device. Any UI
that assumes one agent means one toy needs to list toys from the **devices**
response, not from the agents list.

---

## Notes that will save an afternoon

- **`kid_id` is null for an unpaired toy.** Those sessions belong to no child and
  appear under no child's history. Correct, not a bug — pair the toy first.
- **History before 2026-08-13 13:46 IST (dev) is attributed to the device's
  default character**, so old sessions may show under `Cheeko` when another
  character actually spoke. Nothing on a message row records who spoke, so this
  cannot be repaired. Consider captioning history older than the cutover.
- **`agentName` casing is inconsistent in live data** — `NANI`, `quizzy`,
  `riddler`, `Masti`, `Cheeko`. Title-case it for display rather than trusting the
  stored string.
- **Timestamps are ISO-8601 UTC.** Convert to local before formatting, as the
  models above do.
