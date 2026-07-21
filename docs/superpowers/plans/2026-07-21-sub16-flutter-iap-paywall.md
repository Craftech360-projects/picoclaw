# SUB-16: Parent-App IAP Paywall (purchases_flutter + RevenueCat) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Parents buy/manage the per-device Cheeko subscription inside the Flutter app via RevenueCat (Test Store now, real stores at launch), with the backend flipping the device active through the already-live webhook rail.

**Architecture:** A thin `RevenueCatService` wraps the static `purchases_flutter` SDK (appUserID = selected device MAC). A `SubscriptionProvider` (ChangeNotifier, matching the app's provider pattern) merges backend plans (`GET /toy/api/mobile/subscription/plans`) with RC offering packages by `store_product_id`, runs purchase → poll-until-active, and backs one `SubscriptionScreen` with two modes (paywall / manage). Entry points: Profile menu row + Device Settings banner.

**Tech Stack:** Flutter (Dart ^3.7.2), provider ^6, named routes, `purchases_flutter` ^8, flutter_dotenv, existing `JavaApiService` (Firebase-token auth), backend on `otadev.cheekoai.in` (deploy branch `deploy/otadev-subscription`).

## Global Constraints

- Repos: app `D:\Cheeko-mobile_app\CheekoAI-Parent-App` (branch `feat/iap-subscription`), backend `D:\cheeko-backend\main\manager-api-node` (branch `Subscription_implemetation`, dev deploys via `deploy/otadev-subscription`).
- Product ids (exact, immutable): `cheeko_starter_monthly`, `cheeko_family_monthly`, `cheeko_premium_monthly`. Entitlements: `starter|family|premium`. Offering: `default`.
- **No hardcoded prices anywhere** — always render store-returned localized `priceString` (Apple requirement). Backend `price_inr` is display-fallback only when RC is unreachable, and must be labeled "≈".
- RC SDK key from `.env` (`REVENUECAT_SDK_KEY`); current dev value is the Test Store key `test_JmxvpBvcCpfBKGfGVJvfFtZFkMY` — must never ship in a release build (per-platform `appl_`/`goog_` keys replace it at launch).
- appUserID = `DeviceProvider.selectedDeviceMacAddress` verbatim (colonized uppercase from backend; the webhook normalizes any format). `Purchases.logIn(mac)` on every device switch.
- UX direction A · Sunshine — hero tier = **Family**; reuse `AppTheme` (`SwitzerVariable`, `AppColors.orange` CTA, `Constants.br16`); reference prototype `D:\picoclaw\docs\wayfinder\subscription-system\research\purchase-ux-prototype.html`.
- App response envelope is `{code, msg, data}`; typed `JavaApiService` methods use the `/toy/api/mobile/...` prefix (do NOT use the generic `get()`/`post()` helpers — they lack `/toy`).
- Backend tests run without a live DB (`npx jest` in manager-api-node; unit tests `jest.mock` the prisma config).
- Flutter checks: `flutter analyze` and `flutter test` must pass at every commit.

---

### Task 0: Backend contract additions (plans carry store_product_id; summary carries cancel_at_period_end)

The paywall merges backend plans with RC packages by product id, and the manage view shows "cancels at period end" — neither field is in the current responses.

**Files:**
- Modify: `D:\cheeko-backend\main\manager-api-node\src\services\subscription.service.js` (`getActivePlans` ~line 508; `getSubscriptionSummary` select ~line 530 and return ~line 563)
- Test: `D:\cheeko-backend\main\manager-api-node\tests\unit\subscription.service.test.js`

**Interfaces:**
- Produces: `GET /toy/api/mobile/subscription/plans` items gain `store_product_id: string|null`; `GET /toy/api/mobile/devices/:mac/subscription` data gains `cancel_at_period_end: boolean`.

- [ ] **Step 1: Write the failing tests** (append to the existing file, reusing its `mockPrisma`)

```js
describe('SUB-16 contract additions', () => {
  test('getActivePlans selects store_product_id', async () => {
    mockPrisma.subscription_plans.findMany = jest.fn().mockResolvedValue([]);
    await service.getActivePlans();
    const call = mockPrisma.subscription_plans.findMany.mock.calls[0][0];
    expect(call.select.store_product_id).toBe(true);
  });

  test('getSubscriptionSummary returns cancel_at_period_end', async () => {
    mockPrisma.device_subscriptions.findUnique.mockResolvedValue({
      status: 'active',
      cancel_at_period_end: true,
      trial_started_at: null, trial_ends_at: null, grace_until: null,
      current_period_start: new Date('2026-07-01T00:00:00Z'),
      current_period_end: new Date('2026-08-01T00:00:00Z'),
      subscription_plans: null,
    });
    const summary = await service.getSubscriptionSummary('AA:BB:CC:DD:EE:FF');
    expect(summary.cancel_at_period_end).toBe(true);
  });
});
```

- [ ] **Step 2: Run to verify both fail**

Run: `cd D:\cheeko-backend\main\manager-api-node && npx jest tests/unit/subscription.service.test.js -t "SUB-16 contract"`
Expected: FAIL (`store_product_id` undefined in select; `cancel_at_period_end` undefined in summary)

- [ ] **Step 3: Implement** — in `getActivePlans` change the select to `select: { ...PLAN_LIMIT_SELECT, features: true, store_product_id: true },`. In `getSubscriptionSummary` add `cancel_at_period_end: true,` to the findUnique `select` block, and add `cancel_at_period_end: subscription.cancel_at_period_end,` to the returned object (next to `status`).

- [ ] **Step 4: Run the full backend suite**

Run: `npx jest`
Expected: all green (rate-limit-logging may flake under parallel load — passes alone; pre-existing)

- [ ] **Step 5: Commit + deploy to dev**

```bash
git add tests/unit/subscription.service.test.js src/services/subscription.service.js
git commit -m "feat(subscription): plans expose store_product_id; summary exposes cancel_at_period_end (SUB-16)"
git checkout deploy/otadev-subscription && git merge Subscription_implemetation --no-edit && git push origin Subscription_implemetation deploy/otadev-subscription && git checkout Subscription_implemetation
ssh root@64.227.170.31 "cd /root/xiaozhi-esp32-server && git pull && pm2 restart manager-api"
```

---

### Task 1: App dependencies + config plumbing

**Files:**
- Modify: `pubspec.yaml` (dependencies), `.env` (+ `.env.example` if present)
- Create: `lib/services/revenuecat_keys.dart`
- Test: `test/services/revenuecat_keys_test.dart`

**Interfaces:**
- Produces: `class RevenueCatKeys { static String? sdkKeyFrom(Map<String, String> env); }` returning the value of `REVENUECAT_SDK_KEY` or null.

- [ ] **Step 1: Add the dependency and key**

In `pubspec.yaml` under `dependencies:` add `purchases_flutter: ^8.4.0`. In `.env` add:

```
REVENUECAT_SDK_KEY=test_JmxvpBvcCpfBKGfGVJvfFtZFkMY
```

Run: `flutter pub get` — Expected: resolves cleanly.

- [ ] **Step 2: Write the failing test**

```dart
// test/services/revenuecat_keys_test.dart
import 'package:flutter_test/flutter_test.dart';
import 'package:cheeko_ai/services/revenuecat_keys.dart'; // match the app's package name from pubspec.yaml `name:`

void main() {
  test('reads REVENUECAT_SDK_KEY from an env map', () {
    expect(RevenueCatKeys.sdkKeyFrom({'REVENUECAT_SDK_KEY': 'test_abc'}), 'test_abc');
    expect(RevenueCatKeys.sdkKeyFrom({}), isNull);
  });
}
```

Run: `flutter test test/services/revenuecat_keys_test.dart` — Expected: FAIL (file missing).

- [ ] **Step 3: Implement**

```dart
// lib/services/revenuecat_keys.dart
/// RC public SDK key plumbing. Dev uses the Test Store key from .env;
/// launch swaps to per-platform appl_/goog_ keys (SUB-17).
class RevenueCatKeys {
  static String? sdkKeyFrom(Map<String, String> env) => env['REVENUECAT_SDK_KEY'];
}
```

- [ ] **Step 4: Run** `flutter test test/services/revenuecat_keys_test.dart` — Expected: PASS. Also `flutter analyze` — Expected: no new issues.

- [ ] **Step 5: Commit** `git add pubspec.yaml pubspec.lock .env lib/services/revenuecat_keys.dart test/services/revenuecat_keys_test.dart && git commit -m "feat(iap): purchases_flutter dep + RC sdk key plumbing (SUB-16)"`

---

### Task 2: Subscription models + JavaApiService endpoints

**Files:**
- Create: `lib/models/subscription_plan.dart`, `lib/models/device_subscription.dart`
- Modify: `lib/services/java_api_service.dart` (add two typed methods near `getDeviceSettings`)
- Test: `test/models/subscription_models_test.dart`

**Interfaces:**
- Produces:
  - `SubscriptionPlan { String tier; String name; int? priceInr; String? storeProductId; factory SubscriptionPlan.fromJson(Map<String,dynamic>); }`
  - `DeviceSubscription { String status; bool cancelAtPeriodEnd; DateTime? periodEnd; int? trialDaysLeft; String? planTier; factory DeviceSubscription.fromJson(Map<String,dynamic>); bool get isActive => status == 'active' || status == 'trial' || status == 'grace'; }`
  - `JavaApiService.getSubscriptionPlans() → Future<List<SubscriptionPlan>>` (GET `/toy/api/mobile/subscription/plans`)
  - `JavaApiService.getDeviceSubscription(String mac) → Future<DeviceSubscription?>` (GET `/toy/api/mobile/devices/$mac/subscription`; 404 → null)

- [ ] **Step 1: Write failing model tests**

```dart
// test/models/subscription_models_test.dart
import 'package:flutter_test/flutter_test.dart';
import 'package:cheeko_ai/models/subscription_plan.dart';
import 'package:cheeko_ai/models/device_subscription.dart';

void main() {
  test('SubscriptionPlan parses backend shape', () {
    final p = SubscriptionPlan.fromJson({
      'tier': 'family', 'name': 'Family', 'price_inr': 499,
      'store_product_id': 'cheeko_family_monthly',
      'monthly_question_limit': 900,
    });
    expect(p.tier, 'family');
    expect(p.storeProductId, 'cheeko_family_monthly');
    expect(p.priceInr, 499);
  });

  test('DeviceSubscription parses summary shape and isActive', () {
    final s = DeviceSubscription.fromJson({
      'status': 'trial',
      'cancel_at_period_end': false,
      'plan': {'tier': 'family'},
      'period': {'start': '2026-07-01T00:00:00.000Z', 'end': '2026-08-01T00:00:00.000Z'},
      'trial': {'ends_at': '2026-08-01T00:00:00.000Z', 'days_left': 11},
    });
    expect(s.isActive, true);
    expect(s.trialDaysLeft, 11);
    expect(s.planTier, 'family');
    // missing optional blocks
    final lapsed = DeviceSubscription.fromJson({'status': 'lapsed'});
    expect(lapsed.isActive, false);
    expect(lapsed.cancelAtPeriodEnd, false);
  });
}
```

Run: `flutter test test/models/subscription_models_test.dart` — Expected: FAIL (files missing).

- [ ] **Step 2: Implement the models**

```dart
// lib/models/subscription_plan.dart
class SubscriptionPlan {
  final String tier;
  final String name;
  final int? priceInr;
  final String? storeProductId;

  const SubscriptionPlan({required this.tier, required this.name, this.priceInr, this.storeProductId});

  factory SubscriptionPlan.fromJson(Map<String, dynamic> json) => SubscriptionPlan(
        tier: json['tier'] as String,
        name: (json['name'] ?? json['tier']) as String,
        priceInr: (json['price_inr'] as num?)?.toInt(),
        storeProductId: json['store_product_id'] as String?,
      );
}
```

```dart
// lib/models/device_subscription.dart
class DeviceSubscription {
  final String status;
  final bool cancelAtPeriodEnd;
  final DateTime? periodEnd;
  final int? trialDaysLeft;
  final String? planTier;

  const DeviceSubscription({
    required this.status,
    this.cancelAtPeriodEnd = false,
    this.periodEnd,
    this.trialDaysLeft,
    this.planTier,
  });

  bool get isActive => status == 'active' || status == 'trial' || status == 'grace';

  factory DeviceSubscription.fromJson(Map<String, dynamic> json) => DeviceSubscription(
        status: json['status'] as String,
        cancelAtPeriodEnd: (json['cancel_at_period_end'] as bool?) ?? false,
        periodEnd: json['period']?['end'] != null ? DateTime.tryParse(json['period']['end'] as String) : null,
        trialDaysLeft: (json['trial']?['days_left'] as num?)?.toInt(),
        planTier: json['plan']?['tier'] as String?,
      );
}
```

- [ ] **Step 3: Run model tests** — Expected: PASS.

- [ ] **Step 4: Add the API methods** — in `lib/services/java_api_service.dart`, next to `getDeviceSettings`, following the same `_withTokenRefresh`/envelope pattern used there (adapt names to the file's actual private helpers when editing):

```dart
Future<List<SubscriptionPlan>> getSubscriptionPlans() async {
  final response = await _withTokenRefresh(() async => http.get(
        Uri.parse('$_baseUrl/toy/api/mobile/subscription/plans'),
        headers: await _getHeaders(),
      ));
  final data = _extractListData(response); // if no list-extractor exists, decode body and return (decoded['data'] as List)
  return data.map((e) => SubscriptionPlan.fromJson(e as Map<String, dynamic>)).toList();
}

Future<DeviceSubscription?> getDeviceSubscription(String macAddress) async {
  final response = await _withTokenRefresh(() async => http.get(
        Uri.parse('$_baseUrl/toy/api/mobile/devices/$macAddress/subscription'),
        headers: await _getHeaders(),
      ));
  if (response.statusCode == 404) return null;
  final data = _extractMapData(response);
  return DeviceSubscription.fromJson(data);
}
```

- [ ] **Step 5: Verify + commit**

Run: `flutter analyze` — Expected: clean. Then:
`git add lib/models/subscription_plan.dart lib/models/device_subscription.dart lib/services/java_api_service.dart test/models/subscription_models_test.dart && git commit -m "feat(iap): subscription models + plans/summary API calls (SUB-16)"`

---

### Task 3: RevenueCatService wrapper + device-switch identity

**Files:**
- Create: `lib/services/revenuecat_service.dart`
- Modify: `lib/providers/device_provider.dart` (`selectDevice`, ~line 62), `lib/main.dart` (~line 113 MultiProvider region)
- Test: none beyond `flutter analyze` (the static SDK isn't unit-testable; the wrapper exists precisely so `SubscriptionProvider` can be tested against a fake in Task 4)

**Interfaces:**
- Produces:

```dart
class RevenueCatService {
  Future<void> configure(String sdkKey, String? mac);
  Future<void> logInDevice(String mac);
  Future<Offerings> getOfferings();
  Future<void> purchasePackage(Package pkg);
  Future<CustomerInfo> restorePurchases();
  Future<void> showManageSubscriptions();
}
```

- [ ] **Step 1: Implement the wrapper**

```dart
// lib/services/revenuecat_service.dart
import 'package:flutter/foundation.dart';
import 'package:purchases_flutter/purchases_flutter.dart';

/// Thin instance wrapper over the static purchases_flutter SDK so provider
/// logic can be tested against a fake. appUserID is ALWAYS the device MAC
/// (per-device subscription model): configure() at startup, logInDevice()
/// on every device switch.
class RevenueCatService {
  bool _configured = false;

  Future<void> configure(String sdkKey, String? mac) async {
    if (_configured) return;
    await Purchases.setLogLevel(kReleaseMode ? LogLevel.info : LogLevel.debug);
    final config = PurchasesConfiguration(sdkKey)..appUserID = mac;
    await Purchases.configure(config);
    _configured = true;
  }

  bool get isConfigured => _configured;

  Future<void> logInDevice(String mac) async {
    if (!_configured) return;
    await Purchases.logIn(mac);
  }

  Future<Offerings> getOfferings() => Purchases.getOfferings();

  Future<void> purchasePackage(Package pkg) => Purchases.purchasePackage(pkg);

  Future<CustomerInfo> restorePurchases() => Purchases.restorePurchases();

  Future<void> showManageSubscriptions() => Purchases.showManageSubscriptions();
}
```

- [ ] **Step 2: Register + wire the device switch** — in `main.dart` add to the MultiProvider list: `Provider<RevenueCatService>(create: (_) => RevenueCatService()),` (before `DeviceProvider` consumers). In `device_provider.dart`, add an optional callback so the provider stays SDK-free:

```dart
// field + setter on DeviceProvider:
void Function(String mac)? onDeviceIdentityChanged;

// at the end of selectDevice(Device device), after persisting:
final mac = device.macAddress;
onDeviceIdentityChanged?.call(mac);
```

In `main.dart`, after both providers exist (e.g. in the app bootstrap where `restoreSelectedDevice()` is called), set:

```dart
deviceProvider.onDeviceIdentityChanged = (mac) {
  revenueCatService.logInDevice(mac);
};
```

- [ ] **Step 3: Configure at startup** — where the app finishes provider setup (same bootstrap spot), add:

```dart
final rcKey = RevenueCatKeys.sdkKeyFrom(dotenv.env);
if (rcKey != null && rcKey.isNotEmpty) {
  await revenueCatService.configure(rcKey, deviceProvider.selectedDeviceMacAddress);
} // no key (e.g. CI) → paywall shows backend prices with "≈", purchase disabled
```

- [ ] **Step 4: Verify + commit**

Run: `flutter analyze` — Expected: clean. Launch the app (`flutter run`), check logcat/console for the RC debug "Configured" line with the MAC as app user id.
`git add lib/services/revenuecat_service.dart lib/providers/device_provider.dart lib/main.dart && git commit -m "feat(iap): RevenueCat service, startup configure, logIn on device switch (SUB-16)"`

---

### Task 4: SubscriptionProvider — merge, purchase, poll, restore

**Files:**
- Create: `lib/providers/subscription_provider.dart`
- Test: `test/providers/subscription_provider_test.dart`
- Modify: `lib/main.dart` (register `ChangeNotifierProvider<SubscriptionProvider>`)

**Interfaces:**
- Consumes: `JavaApiService.getSubscriptionPlans/getDeviceSubscription` (Task 2), `RevenueCatService` (Task 3).
- Produces (what the screen in Task 5 uses):

```dart
enum PurchaseFlowState { idle, purchasing, waitingForActivation, success, error }

class PaywallTier {
  final SubscriptionPlan plan;
  final Package? rcPackage;      // null when RC unavailable
  final String priceLabel;       // rcPackage.storeProduct.priceString, else '≈ ₹<price_inr>'
  bool get purchasable => rcPackage != null;
}

class SubscriptionProvider extends ChangeNotifier {
  SubscriptionProvider({required JavaApiService api, required RevenueCatService rc,
      Duration pollInterval = const Duration(seconds: 2), int maxPolls = 10});
  List<PaywallTier> tiers;                 // sorted starter→premium
  DeviceSubscription? current;             // for the selected mac
  PurchaseFlowState flow; String? errorMessage; bool alreadySubscribedElsewhere;
  Future<void> load(String mac);           // plans + offerings + summary
  Future<void> purchase(String mac, PaywallTier tier); // purchase → poll until active
  Future<void> restore(String mac);        // restorePurchases → poll once
  Future<void> openManage();               // rc.showManageSubscriptions
}
```

- [ ] **Step 1: Write the failing tests** (hand-rolled fakes; no mockito)

```dart
// test/providers/subscription_provider_test.dart
import 'package:flutter_test/flutter_test.dart';
import 'package:cheeko_ai/models/subscription_plan.dart';
import 'package:cheeko_ai/models/device_subscription.dart';
import 'package:cheeko_ai/providers/subscription_provider.dart';

class _FakeApi implements SubscriptionApi {
  List<DeviceSubscription?> summaries = [];
  int summaryCalls = 0;
  @override
  Future<List<SubscriptionPlan>> getSubscriptionPlans() async => [
        const SubscriptionPlan(tier: 'starter', name: 'Starter', priceInr: 199, storeProductId: 'cheeko_starter_monthly'),
        const SubscriptionPlan(tier: 'family', name: 'Family', priceInr: 499, storeProductId: 'cheeko_family_monthly'),
        const SubscriptionPlan(tier: 'premium', name: 'Premium', priceInr: 999, storeProductId: 'cheeko_premium_monthly'),
      ];
  @override
  Future<DeviceSubscription?> getDeviceSubscription(String mac) async {
    summaryCalls++;
    return summaries.isEmpty ? null : summaries.removeAt(0);
  }
}

class _FakeRc implements SubscriptionStore {
  bool offeringsThrow = false;
  Object? purchaseError;
  @override
  Future<Map<String, StorePackage>> packagesByProductId() async {
    if (offeringsThrow) throw Exception('rc down');
    return {
      'cheeko_family_monthly': const StorePackage(productId: 'cheeko_family_monthly', priceString: '\$5.99', raw: null),
      'cheeko_starter_monthly': const StorePackage(productId: 'cheeko_starter_monthly', priceString: '\$2.49', raw: null),
      'cheeko_premium_monthly': const StorePackage(productId: 'cheeko_premium_monthly', priceString: '\$11.99', raw: null),
    };
  }
  @override
  Future<void> purchase(StorePackage pkg) async {
    if (purchaseError != null) throw purchaseError!;
  }
  @override
  Future<void> restore() async {}
  @override
  Future<void> openManage() async {}
}

SubscriptionProvider make(_FakeApi api, _FakeRc rc) => SubscriptionProvider(
    api: api, rc: rc, pollInterval: Duration.zero, maxPolls: 3);

void main() {
  test('load merges plans with store prices by product id, starter→premium', () async {
    final p = make(_FakeApi(), _FakeRc());
    await p.load('AA:BB:CC:DD:EE:FF');
    expect(p.tiers.map((t) => t.plan.tier).toList(), ['starter', 'family', 'premium']);
    expect(p.tiers[1].priceLabel, '\$5.99');
    expect(p.tiers[1].purchasable, true);
  });

  test('RC unreachable → backend price fallback labeled ≈, not purchasable', () async {
    final rc = _FakeRc()..offeringsThrow = true;
    final p = make(_FakeApi(), rc);
    await p.load('AA:BB:CC:DD:EE:FF');
    expect(p.tiers[1].priceLabel, '≈ ₹499');
    expect(p.tiers[1].purchasable, false);
  });

  test('purchase polls summary until active then success', () async {
    final api = _FakeApi()
      ..summaries = [
        null, // initial load
        const DeviceSubscription(status: 'trial'),          // poll 1: webhook not landed
        const DeviceSubscription(status: 'active', planTier: 'family'), // poll 2
      ];
    final p = make(api, _FakeRc());
    await p.load('AA:BB:CC:DD:EE:FF');
    await p.purchase('AA:BB:CC:DD:EE:FF', p.tiers[1]);
    expect(p.flow, PurchaseFlowState.success);
    expect(p.current?.status, 'active');
  });

  test('webhook never lands within maxPolls → error state, not success', () async {
    final api = _FakeApi()..summaries = [null];
    final p = make(api, _FakeRc());
    await p.load('AA:BB:CC:DD:EE:FF');
    await p.purchase('AA:BB:CC:DD:EE:FF', p.tiers[1]);
    expect(p.flow, PurchaseFlowState.error);
    expect(p.errorMessage, isNotNull);
  });

  test('already-subscribed store account maps to the ceiling flag', () async {
    final rc = _FakeRc()..purchaseError = const AlreadySubscribedException();
    final p = make(_FakeApi(), rc);
    await p.load('AA:BB:CC:DD:EE:FF');
    await p.purchase('AA:BB:CC:DD:EE:FF', p.tiers[1]);
    expect(p.alreadySubscribedElsewhere, true);
    expect(p.flow, PurchaseFlowState.error);
  });

  test('user cancels the store sheet → back to idle, no error banner', () async {
    final rc = _FakeRc()..purchaseError = const PurchaseCancelledException();
    final p = make(_FakeApi(), rc);
    await p.load('AA:BB:CC:DD:EE:FF');
    await p.purchase('AA:BB:CC:DD:EE:FF', p.tiers[1]);
    expect(p.flow, PurchaseFlowState.idle);
  });
}
```

Run: `flutter test test/providers/subscription_provider_test.dart` — Expected: FAIL (provider missing).

- [ ] **Step 2: Implement the provider.** Key structure — the SDK-facing seam is two small interfaces the fakes implement; the real adapters live in the same file:

```dart
// lib/providers/subscription_provider.dart
import 'package:flutter/foundation.dart';
import 'package:purchases_flutter/purchases_flutter.dart';
import '../models/device_subscription.dart';
import '../models/subscription_plan.dart';
import '../services/java_api_service.dart';
import '../services/revenuecat_service.dart';

/// Seams so tests can fake the backend + store without the static SDK.
abstract class SubscriptionApi {
  Future<List<SubscriptionPlan>> getSubscriptionPlans();
  Future<DeviceSubscription?> getDeviceSubscription(String mac);
}

class StorePackage {
  final String productId;
  final String priceString;
  final Package? raw; // null in tests
  const StorePackage({required this.productId, required this.priceString, required this.raw});
}

abstract class SubscriptionStore {
  Future<Map<String, StorePackage>> packagesByProductId();
  Future<void> purchase(StorePackage pkg);
  Future<void> restore();
  Future<void> openManage();
}

class AlreadySubscribedException implements Exception { const AlreadySubscribedException(); }
class PurchaseCancelledException implements Exception { const PurchaseCancelledException(); }

enum PurchaseFlowState { idle, purchasing, waitingForActivation, success, error }

class PaywallTier {
  final SubscriptionPlan plan;
  final StorePackage? rcPackage;
  const PaywallTier({required this.plan, this.rcPackage});
  bool get purchasable => rcPackage != null;
  String get priceLabel => rcPackage?.priceString ?? '≈ ₹${plan.priceInr ?? '—'}';
}

class SubscriptionProvider extends ChangeNotifier {
  SubscriptionProvider({
    required SubscriptionApi api,
    required SubscriptionStore rc,
    this.pollInterval = const Duration(seconds: 2),
    this.maxPolls = 10,
  })  : _api = api, _rc = rc;

  final SubscriptionApi _api;
  final SubscriptionStore _rc;
  final Duration pollInterval;
  final int maxPolls;

  static const _tierOrder = ['starter', 'family', 'premium'];

  List<PaywallTier> tiers = [];
  DeviceSubscription? current;
  PurchaseFlowState flow = PurchaseFlowState.idle;
  String? errorMessage;
  bool alreadySubscribedElsewhere = false;
  bool loading = false;

  Future<void> load(String mac) async {
    loading = true; notifyListeners();
    try {
      final plans = await _api.getSubscriptionPlans();
      Map<String, StorePackage> packages = {};
      try {
        packages = await _rc.packagesByProductId();
      } catch (_) {/* RC down → backend-price fallback, purchase disabled */}
      plans.sort((a, b) => _tierOrder.indexOf(a.tier).compareTo(_tierOrder.indexOf(b.tier)));
      tiers = plans
          .map((p) => PaywallTier(plan: p, rcPackage: packages[p.storeProductId]))
          .toList();
      current = await _api.getDeviceSubscription(mac);
    } finally {
      loading = false; notifyListeners();
    }
  }

  Future<void> purchase(String mac, PaywallTier tier) async {
    if (!tier.purchasable) return;
    flow = PurchaseFlowState.purchasing; errorMessage = null;
    alreadySubscribedElsewhere = false; notifyListeners();
    try {
      await _rc.purchase(tier.rcPackage!);
    } on PurchaseCancelledException {
      flow = PurchaseFlowState.idle; notifyListeners(); return;
    } on AlreadySubscribedException {
      // Per-device ceiling: one subscribed device per store account (spec).
      alreadySubscribedElsewhere = true;
      flow = PurchaseFlowState.error;
      errorMessage = 'This App Store / Google account already has a Cheeko subscription for another device.';
      notifyListeners(); return;
    } catch (e) {
      flow = PurchaseFlowState.error;
      errorMessage = 'Purchase failed. You were not charged — please try again.';
      notifyListeners(); return;
    }
    // Store purchase succeeded — the backend flips on the RC webhook. Poll.
    flow = PurchaseFlowState.waitingForActivation; notifyListeners();
    for (var i = 0; i < maxPolls; i++) {
      await Future<void>.delayed(pollInterval);
      final summary = await _api.getDeviceSubscription(mac);
      if (summary != null && summary.status == 'active') {
        current = summary; flow = PurchaseFlowState.success; notifyListeners(); return;
      }
    }
    flow = PurchaseFlowState.error;
    errorMessage =
        'Payment received — activation is taking longer than usual. It will complete automatically; pull to refresh in a minute.';
    notifyListeners();
  }

  Future<void> restore(String mac) async {
    flow = PurchaseFlowState.purchasing; notifyListeners();
    try {
      await _rc.restore();
      current = await _api.getDeviceSubscription(mac);
      flow = (current?.status == 'active') ? PurchaseFlowState.success : PurchaseFlowState.idle;
    } catch (e) {
      flow = PurchaseFlowState.error;
      errorMessage = 'Nothing to restore on this store account.';
    }
    notifyListeners();
  }

  Future<void> openManage() => _rc.openManage();
}

/// Real adapters over the app services (constructed in main.dart).
class JavaSubscriptionApi implements SubscriptionApi {
  JavaSubscriptionApi(this._api);
  final JavaApiService _api;
  @override
  Future<List<SubscriptionPlan>> getSubscriptionPlans() => _api.getSubscriptionPlans();
  @override
  Future<DeviceSubscription?> getDeviceSubscription(String mac) => _api.getDeviceSubscription(mac);
}

class RevenueCatSubscriptionStore implements SubscriptionStore {
  RevenueCatSubscriptionStore(this._rc);
  final RevenueCatService _rc;

  @override
  Future<Map<String, StorePackage>> packagesByProductId() async {
    final offerings = await _rc.getOfferings();
    final pkgs = offerings.current?.availablePackages ?? const <Package>[];
    return {
      for (final p in pkgs)
        p.storeProduct.identifier:
            StorePackage(productId: p.storeProduct.identifier, priceString: p.storeProduct.priceString, raw: p),
    };
  }

  @override
  Future<void> purchase(StorePackage pkg) async {
    try {
      await _rc.purchasePackage(pkg.raw!);
    } on PlatformException catch (e) {
      final code = PurchasesErrorHelper.getErrorCode(e);
      if (code == PurchasesErrorCode.purchaseCancelledError) throw const PurchaseCancelledException();
      if (code == PurchasesErrorCode.productAlreadyPurchasedError) throw const AlreadySubscribedException();
      rethrow;
    }
  }

  @override
  Future<void> restore() => _rc.restorePurchases();
  @override
  Future<void> openManage() => _rc.showManageSubscriptions();
}
```

(Add `import 'package:flutter/services.dart';` for `PlatformException`.)

- [ ] **Step 3: Run** `flutter test test/providers/subscription_provider_test.dart` — Expected: all 6 PASS.

- [ ] **Step 4: Register in main.dart** — add to MultiProvider (after JavaApiService/RevenueCatService):

```dart
ChangeNotifierProvider<SubscriptionProvider>(
  create: (ctx) => SubscriptionProvider(
    api: JavaSubscriptionApi(ctx.read<JavaApiService>()),
    rc: RevenueCatSubscriptionStore(ctx.read<RevenueCatService>()),
  ),
),
```

- [ ] **Step 5: Verify + commit**

Run: `flutter analyze && flutter test` — Expected: clean/green.
`git add lib/providers/subscription_provider.dart test/providers/subscription_provider_test.dart lib/main.dart && git commit -m "feat(iap): SubscriptionProvider — plan/price merge, purchase+poll, restore, ceiling copy (SUB-16)"`

---

### Task 5: SubscriptionScreen (paywall + manage modes)

**Files:**
- Create: `lib/screens/subscription/subscription_screen.dart`
- Test: `test/screens/subscription_screen_test.dart`

**Interfaces:**
- Consumes: `SubscriptionProvider` (Task 4), `DeviceProvider.selectedDeviceMacAddress`, `AppTheme`/`AppColors`/`Constants`.
- Produces: `class SubscriptionScreen extends StatefulWidget` registered later as route `/subscription`. Visual spec: Sunshine direction (see the prototype HTML in Global Constraints); hero = Family.

- [ ] **Step 1: Write the failing widget test**

```dart
// test/screens/subscription_screen_test.dart
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:provider/provider.dart';
import 'package:cheeko_ai/models/subscription_plan.dart';
import 'package:cheeko_ai/providers/subscription_provider.dart';
import 'package:cheeko_ai/screens/subscription/subscription_screen.dart';

class _StubProvider extends SubscriptionProvider {
  _StubProvider() : super(api: _NullApi(), rc: _NullStore());
  @override
  Future<void> load(String mac) async {}
}

class _NullApi implements SubscriptionApi {
  @override
  Future<List<SubscriptionPlan>> getSubscriptionPlans() async => [];
  @override
  Future<dynamic> getDeviceSubscription(String mac) async => null;
}

class _NullStore implements SubscriptionStore {
  @override
  Future<Map<String, StorePackage>> packagesByProductId() async => {};
  @override
  Future<void> purchase(StorePackage pkg) async {}
  @override
  Future<void> restore() async {}
  @override
  Future<void> openManage() async {}
}

Widget host(SubscriptionProvider p) => ChangeNotifierProvider<SubscriptionProvider>.value(
    value: p, child: const MaterialApp(home: SubscriptionScreen(macAddress: 'AA:BB:CC:DD:EE:FF')));

void main() {
  testWidgets('paywall renders 3 tiers with store prices, Family marked popular', (tester) async {
    final p = _StubProvider()
      ..tiers = [
        const PaywallTier(plan: SubscriptionPlan(tier: 'starter', name: 'Starter', priceInr: 199),
            rcPackage: StorePackage(productId: 'cheeko_starter_monthly', priceString: '\$2.49', raw: null)),
        const PaywallTier(plan: SubscriptionPlan(tier: 'family', name: 'Family', priceInr: 499),
            rcPackage: StorePackage(productId: 'cheeko_family_monthly', priceString: '\$5.99', raw: null)),
        const PaywallTier(plan: SubscriptionPlan(tier: 'premium', name: 'Premium', priceInr: 999),
            rcPackage: StorePackage(productId: 'cheeko_premium_monthly', priceString: '\$11.99', raw: null)),
      ];
    await tester.pumpWidget(host(p));
    await tester.pump();
    expect(find.text('Starter'), findsOneWidget);
    expect(find.text('\$5.99'), findsOneWidget);
    expect(find.text('Most popular'), findsOneWidget); // Family hero badge
    expect(find.text('Restore purchases'), findsOneWidget);
  });

  testWidgets('active subscription shows manage mode, not tier cards', (tester) async {
    final p = _StubProvider()
      ..current = const DeviceSubscription(status: 'active', planTier: 'family',
          cancelAtPeriodEnd: true, periodEnd: null);
    await tester.pumpWidget(host(p));
    await tester.pump();
    expect(find.textContaining('Family'), findsWidgets);
    expect(find.textContaining('will not renew'), findsOneWidget); // cancel_at_period_end copy
    expect(find.text('Manage in store'), findsOneWidget);
    expect(find.text('Most popular'), findsNothing);
  });
}
```

(Add `import 'package:cheeko_ai/models/device_subscription.dart';`.) Run: `flutter test test/screens/subscription_screen_test.dart` — Expected: FAIL.

- [ ] **Step 2: Implement the screen.** One stateful widget, three sections by state; reuse the app theme throughout (`Theme.of(context).textTheme`, `AppColors.orange` CTA, cards with `Constants.br16`).

```dart
// lib/screens/subscription/subscription_screen.dart
import 'package:flutter/material.dart';
import 'package:provider/provider.dart';
import '../../core/constants/constants.dart';
import '../../core/themes/app_colors.dart';
import '../../providers/subscription_provider.dart';

class SubscriptionScreen extends StatefulWidget {
  const SubscriptionScreen({super.key, required this.macAddress});
  final String macAddress;

  @override
  State<SubscriptionScreen> createState() => _SubscriptionScreenState();
}

class _SubscriptionScreenState extends State<SubscriptionScreen> {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback(
        (_) => context.read<SubscriptionProvider>().load(widget.macAddress));
  }

  @override
  Widget build(BuildContext context) {
    final p = context.watch<SubscriptionProvider>();
    return Scaffold(
      backgroundColor: AppColors.background,
      appBar: AppBar(title: const Text('Cheeko Plans')),
      body: p.loading
          ? const Center(child: CircularProgressIndicator())
          : p.flow == PurchaseFlowState.success
              ? _Celebration(planTier: p.current?.planTier)
              : (p.current?.status == 'active')
                  ? _ManageView(p: p)
                  : _PaywallView(p: p, mac: widget.macAddress),
    );
  }
}

class _PaywallView extends StatelessWidget {
  const _PaywallView({required this.p, required this.mac});
  final SubscriptionProvider p;
  final String mac;

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context).textTheme;
    final busy = p.flow == PurchaseFlowState.purchasing ||
        p.flow == PurchaseFlowState.waitingForActivation;
    return ListView(
      padding: const EdgeInsets.all(20),
      children: [
        if (p.current?.status == 'trial' && p.current?.trialDaysLeft != null)
          _Banner(text: 'Free trial — ${p.current!.trialDaysLeft} days left. Pick a plan to keep going.'),
        if (p.current != null && !p.current!.isActive)
          _Banner(text: 'Cheeko is waiting! Choose a plan to start the conversations again.'),
        for (final tier in p.tiers) _TierCard(tier: tier, busy: busy, onBuy: () => p.purchase(mac, tier)),
        if (p.flow == PurchaseFlowState.waitingForActivation)
          const Padding(
            padding: EdgeInsets.all(12),
            child: Text('Confirming with the store…', textAlign: TextAlign.center),
          ),
        if (p.errorMessage != null)
          Padding(
            padding: const EdgeInsets.all(12),
            child: Text(p.errorMessage!,
                textAlign: TextAlign.center, style: t.bodyMedium?.copyWith(color: Colors.red)),
          ),
        TextButton(
          onPressed: busy ? null : () => p.restore(mac),
          child: const Text('Restore purchases'),
        ),
        Text(
          'Billed monthly through your App Store / Google Play account. Cancel anytime in your store settings.',
          textAlign: TextAlign.center,
          style: t.bodySmall?.copyWith(color: AppColors.darkGrey),
        ),
      ],
    );
  }
}

class _TierCard extends StatelessWidget {
  const _TierCard({required this.tier, required this.busy, required this.onBuy});
  final PaywallTier tier;
  final bool busy;
  final VoidCallback onBuy;

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context).textTheme;
    final isHero = tier.plan.tier == 'family';
    return Container(
      margin: const EdgeInsets.only(bottom: 14),
      decoration: BoxDecoration(
        color: Colors.white,
        borderRadius: BorderRadius.circular(Constants.br16),
        border: isHero ? Border.all(color: AppColors.orange, width: 2) : null,
      ),
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(children: [
            Text(tier.plan.name, style: t.headlineSmall),
            const Spacer(),
            if (isHero)
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
                decoration: BoxDecoration(
                    color: AppColors.orange, borderRadius: BorderRadius.circular(Constants.br8)),
                child: const Text('Most popular',
                    style: TextStyle(color: Colors.white, fontSize: 12, fontWeight: FontWeight.w600)),
              ),
          ]),
          const SizedBox(height: 6),
          Text('${tier.priceLabel} / month', style: t.titleLarge),
          const SizedBox(height: 12),
          SizedBox(
            width: double.infinity,
            child: ElevatedButton(
              onPressed: (busy || !tier.purchasable) ? null : onBuy,
              child: Text('Choose ${tier.plan.name}'),
            ),
          ),
        ],
      ),
    );
  }
}

class _ManageView extends StatelessWidget {
  const _ManageView({required this.p});
  final SubscriptionProvider p;

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context).textTheme;
    final sub = p.current!;
    final renew = sub.periodEnd != null
        ? '${sub.periodEnd!.day}/${sub.periodEnd!.month}/${sub.periodEnd!.year}'
        : '—';
    return ListView(padding: const EdgeInsets.all(20), children: [
      Text('Current plan: ${sub.planTier ?? '—'}', style: t.headlineSmall),
      const SizedBox(height: 8),
      Text(sub.cancelAtPeriodEnd
          ? 'Your plan will not renew — it runs until $renew. Re-subscribe anytime.'
          : 'Renews on $renew.'),
      const SizedBox(height: 20),
      ElevatedButton(onPressed: p.openManage, child: const Text('Manage in store')),
      const SizedBox(height: 8),
      Text(
        'Upgrades, downgrades and cancellation are handled by your App Store / Google Play account.',
        style: t.bodySmall?.copyWith(color: AppColors.darkGrey),
      ),
    ]);
  }
}

class _Celebration extends StatelessWidget {
  const _Celebration({required this.planTier});
  final String? planTier;
  @override
  Widget build(BuildContext context) => Center(
        child: Column(mainAxisAlignment: MainAxisAlignment.center, children: [
          const Text('🎉', style: TextStyle(fontSize: 64)),
          const SizedBox(height: 12),
          Text('${planTier ?? 'Plan'} is active!',
              style: Theme.of(context).textTheme.headlineMedium),
          const SizedBox(height: 8),
          const Text('Cheeko is ready to play.'),
          const SizedBox(height: 20),
          ElevatedButton(onPressed: () => Navigator.pop(context), child: const Text('Done')),
        ]),
      );
}

class _Banner extends StatelessWidget {
  const _Banner({required this.text});
  final String text;
  @override
  Widget build(BuildContext context) => Container(
        margin: const EdgeInsets.only(bottom: 16),
        padding: const EdgeInsets.all(12),
        decoration: BoxDecoration(
            color: AppColors.goldenYellow.withValues(alpha: 0.2),
            borderRadius: BorderRadius.circular(Constants.br8)),
        child: Text(text),
      );
}
```

- [ ] **Step 3: Run** `flutter test test/screens/subscription_screen_test.dart` — Expected: PASS (adjust the "will not renew" copy or test string to match exactly — they must agree).

- [ ] **Step 4: Commit** `git add lib/screens/subscription/ test/screens/subscription_screen_test.dart && git commit -m "feat(iap): SubscriptionScreen — paywall/manage/success states, Sunshine styling (SUB-16)"`

---

### Task 6: Route + entry points + live Test Store e2e

**Files:**
- Modify: `lib/routes/routes.dart` (`AppRoutes` + `onGenerateRoute`), `lib/screens/profile/parent_profile.dart` (menu list ~line 519–545), `lib/screens/device/device_settings_screen.dart` (top of settings list)

**Interfaces:**
- Consumes: `SubscriptionScreen(macAddress:)` (Task 5), `DeviceProvider.selectedDeviceMacAddress`.
- Produces: `AppRoutes.subscription = '/subscription'`, navigated with `Navigator.pushNamed(context, AppRoutes.subscription)` (screen reads the selected MAC itself if no argument is passed — pass `arguments: mac` from device settings to pin a specific device).

- [ ] **Step 1: Register the route** — in `routes.dart` add `static const String subscription = '/subscription';` and a case:

```dart
case AppRoutes.subscription:
  final mac = (settings.arguments as String?) ??
      // fall back to the globally selected device
      navigatorKey.currentContext?.read<DeviceProvider>()?.selectedDeviceMacAddress;
  // If the file has no navigatorKey pattern, instead require the argument and
  // always pass it from call sites (both entry points below pass it).
  return MaterialPageRoute(
      builder: (_) => SubscriptionScreen(macAddress: mac ?? ''));
```

- [ ] **Step 2: Entry points.** In `parent_profile.dart`, add a menu row following the file's existing `_MenuItem`/ListTile pattern: icon `Icons.workspace_premium`, label `'Subscription'`, onTap `Navigator.pushNamed(context, AppRoutes.subscription, arguments: context.read<DeviceProvider>().selectedDeviceMacAddress)`. In `device_settings_screen.dart`, add a tappable status row near the top: `'Plan & usage'` → same navigation with that screen's device MAC argument.

- [ ] **Step 3: Full live e2e against the Test Store + otadev** (manual, with the app pointed at the dev backend via Developer Options):
  1. Run the app on a device/emulator, sign in, select a bound device.
  2. Open Profile → Subscription: 3 tiers render with **Test Store prices** ($2.49/$5.99/$11.99), Family framed orange with "Most popular".
  3. Buy Family → RC test-store sheet completes → "Confirming with the store…" → within ~one poll cycle the celebration screen shows (webhook → otadev flips the row active).
  4. Verify on the backend: `ssh root@64.227.170.31 "grep REVENUECAT /root/.pm2/logs/manager-api-out.log | tail -5"` shows the INITIAL_PURCHASE applied for the device MAC.
  5. Reopen the screen: manage mode with plan + renewal date.
  6. Tap Restore purchases: completes without error.
- Expected artifacts: screenshot of paywall + celebration; the webhook log line. Record all in the ticket at close.

- [ ] **Step 4: Verify + commit**

Run: `flutter analyze && flutter test` — Expected: clean/green.
`git add lib/routes/routes.dart lib/screens/profile/parent_profile.dart lib/screens/device/device_settings_screen.dart && git commit -m "feat(iap): subscription route + profile/device-settings entry points (SUB-16)"`

---

### Task 7: Close the ticket

**Files:**
- Modify: `D:\picoclaw\docs\issues\subscription\016-flutter-iap-paywall.md`

- [ ] **Step 1:** Set `status: closed`, tick the criteria that passed on the Test Store, and mark these as **deferred to real-store readiness (SUB-17)**: iOS/Android sandbox purchases, store-review pass, store-native cancel deep-link verification on real stores. Append a `## Resolution` with commits, the e2e evidence from Task 6, and the launch checklist item: "swap `REVENUECAT_SDK_KEY` to per-platform `appl_`/`goog_` keys + point RC webhook at prod".
- [ ] **Step 2:** Commit in picoclaw: `git add docs/issues/subscription/016-flutter-iap-paywall.md && git commit -m "docs(subscription): close SUB-16 — Test Store paywall shipped"`

---

## Self-review notes

- Ticket coverage: plans+prices merge (T2/T4/T5), hero=Family + trial banner (T5), purchase → poll → celebration (T4/T5), manage + store deep-link + `cancel_at_period_end` copy (T0/T5), restore (T4/T5), ceiling copy for already-subscribed account (T4), no hardcoded prices (T4 `priceLabel`), entry points (T6). Store-sandbox/review criteria deferred — blocked on SUB-17 real-store verification, stated in T7.
- The `_extractListData`/`_withTokenRefresh` names in Task 2 must be adapted to `java_api_service.dart`'s actual private helpers — the implementer edits inside that file and follows its local pattern (recon: `getDeviceSettings` is the model).
- Type check: `SubscriptionApi`/`SubscriptionStore`/`StorePackage`/`PaywallTier`/`PurchaseFlowState` defined once in Task 4 and consumed by Task 5's tests/screen; `DeviceSubscription.isActive` from Task 2 used in T4/T5.
