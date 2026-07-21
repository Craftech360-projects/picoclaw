# SUB-15: RevenueCat Webhook Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `POST /webhooks/revenuecat` turns RevenueCat events into `device_subscriptions` state so an IAP purchase activates a device.

**Architecture:** Mirror of the shipped Razorpay pair (`src/services/razorpay.service.js` + `src/routes/razorpayWebhook.routes.js`, SUB-6): route authenticates and hands off; service ledgers into `subscription_events` (unique-key dedupe) then applies transitions with a forward-only period-anchor guard. RC identity: `event.app_user_id` **is** the device MAC (design doc). No raw-body HMAC — RC auth is a static `Authorization` header compare, so the route mounts after the JSON parser.

**Tech Stack:** Node/Express, Prisma (PostgreSQL), Jest + supertest. No new dependencies.

**Repo / branch:** `D:\cheeko-backend\main\manager-api-node`, branch `Subscription_implemetation`.

## Global Constraints

- Spec: `D:\picoclaw\docs\superpowers\specs\2026-07-21-iap-subscription-rails-design.md`; ticket: `D:\picoclaw\docs\issues\subscription\015-revenuecat-backend.md`.
- Tests run WITHOUT a live DB (`tests/setup.js` convention): unit tests mock `../../src/config/database`; integration tests only exercise the gate in front of the ledger.
- Period anchors only ever move forward (same guard as `applyActiveState` in `razorpay.service.js:203-212`).
- `BILLING_ISSUE` and refunds are **ledgered only** — transitions are SUB-7 scope. Unknown event types ⇒ ledger + 200.
- Env vars: `REVENUECAT_WEBHOOK_AUTH` (webhook auth), documented in `.env.example`, never committed to `.env`.
- Windows/jest gotcha: never run two jest processes concurrently (cache deadlock). If a run hangs, kill stale `node.exe` jest runners first.
- Commit style: `feat(subscription): <what> (SUB-15)`.

---

### Task 1: Schema migration — store columns

**Files:**
- Modify: `prisma/schema.prisma` (models `subscription_plans` ~line 1384, `device_subscriptions` ~line 1404)
- Create: `prisma/migrations/20260721000000_sub15_revenuecat_columns/migration.sql`
- Modify: `.env.example` (append RevenueCat block)

**Interfaces:**
- Produces: columns `subscription_plans.store_product_id String?`, `device_subscriptions.store String?`, `device_subscriptions.rc_original_transaction_id String?` — Task 2+ query plans by `store_product_id` and write `store`/`rc_original_transaction_id`.

- [ ] **Step 1: Edit `prisma/schema.prisma`**

In `model subscription_plans`, after `razorpay_plan_id       String?` add:

```prisma
  /// IAP product id, identical on both stores (SUB-17 seeds it), e.g. cheeko_family_monthly
  store_product_id       String?
```

In `model device_subscriptions`, after `razorpay_subscription_id String?` add:

```prisma
  /// Which rails activated this row: app_store | play_store (null = razorpay-era/trial)
  store                    String?
  rc_original_transaction_id String?
```

- [ ] **Step 2: Write the migration SQL**

`prisma/migrations/20260721000000_sub15_revenuecat_columns/migration.sql`:

```sql
-- SUB-15: RevenueCat/IAP columns
ALTER TABLE "subscription_plans" ADD COLUMN "store_product_id" TEXT;
ALTER TABLE "device_subscriptions" ADD COLUMN "store" TEXT;
ALTER TABLE "device_subscriptions" ADD COLUMN "rc_original_transaction_id" TEXT;

-- Seed product ids for the 3 launch tiers (SUB-17 creates the same ids in both consoles)
UPDATE "subscription_plans" SET "store_product_id" = 'cheeko_' || "tier" || '_monthly'
WHERE "tier" IN ('starter', 'family', 'premium');
```

- [ ] **Step 3: Validate schema**

Run: `npx prisma validate`
Expected: `The schema at prisma\schema.prisma is valid`
(Do NOT run `prisma migrate dev` — no live DB in this environment; migration applies at deploy, same as SUB-2..5 migrations.)

- [ ] **Step 4: Append to `.env.example`**

```bash
# RevenueCat (SUB-15) — value RC sends verbatim in the Authorization header of webhooks
REVENUECAT_WEBHOOK_AUTH=
```

- [ ] **Step 5: Commit**

```bash
git add prisma/schema.prisma prisma/migrations/20260721000000_sub15_revenuecat_columns/migration.sql .env.example
git commit -m "feat(subscription): schema columns for RevenueCat rails (SUB-15)"
```

---

### Task 2: Service — auth check, ledger dedupe, active transitions

**Files:**
- Create: `src/services/revenuecat.service.js`
- Test: `tests/unit/revenuecat.service.test.js`

**Interfaces:**
- Consumes: Task 1 columns; `normalizeMacAddress` from `src/utils/helpers`; `prisma` from `src/config/database`; `logger` from `src/utils/logger`.
- Produces: `verifyWebhookAuth(headerValue) => boolean`, `processWebhookEvent(event) => Promise<{outcome: 'duplicate'|'processed'|'ledgered'}>` — Task 3's route calls exactly these. Ledger key: `subscription_events.razorpay_event_id = 'rc:' + event.id` (column reused; prefix prevents any collision with Razorpay ids).

RevenueCat webhook body is `{api_version, event}`; the route passes `body.event` here. Fields used: `id, type, app_user_id, product_id, new_product_id, purchased_at_ms, expiration_at_ms, store, original_transaction_id, environment`.

- [ ] **Step 1: Write failing tests**

`tests/unit/revenuecat.service.test.js`:

```javascript
/**
 * RevenueCat service unit tests (SUB-15): webhook auth compare, idempotent
 * ledger, INITIAL_PURCHASE/RENEWAL activation with the forward-only anchor
 * guard, plan mapping by store_product_id, and the ledger-only default.
 */

const mockPrisma = {
  device_subscriptions: {
    upsert: jest.fn(),
    updateMany: jest.fn().mockResolvedValue({ count: 1 }),
  },
  subscription_plans: {
    findFirst: jest.fn(),
  },
  subscription_events: {
    createMany: jest.fn().mockResolvedValue({ count: 1 }),
    updateMany: jest.fn().mockResolvedValue({ count: 1 }),
  },
};
jest.mock('../../src/config/database', () => ({ prisma: mockPrisma }));

const service = require('../../src/services/revenuecat.service');

const MAC = 'AA:BB:CC:DD:EE:FF';
const AUTH = 'rc-webhook-secret';

const rcEvent = (overrides = {}) => ({
  id: 'evt_rc_1',
  type: 'INITIAL_PURCHASE',
  app_user_id: MAC,
  product_id: 'cheeko_family_monthly',
  purchased_at_ms: 1752700000000,
  expiration_at_ms: 1755378400000,
  store: 'APP_STORE',
  original_transaction_id: 'txn_1',
  environment: 'SANDBOX',
  ...overrides,
});

beforeEach(() => {
  jest.clearAllMocks();
  mockPrisma.subscription_events.createMany.mockResolvedValue({ count: 1 });
  mockPrisma.subscription_events.updateMany.mockResolvedValue({ count: 1 });
  mockPrisma.device_subscriptions.updateMany.mockResolvedValue({ count: 1 });
  mockPrisma.subscription_plans.findFirst.mockResolvedValue({ id: 2n });
  process.env.REVENUECAT_WEBHOOK_AUTH = AUTH;
});

afterEach(() => {
  delete process.env.REVENUECAT_WEBHOOK_AUTH;
});

describe('verifyWebhookAuth', () => {
  test('accepts the exact configured value', () => {
    expect(service.verifyWebhookAuth(AUTH)).toBe(true);
  });
  test('rejects a wrong value', () => {
    expect(service.verifyWebhookAuth('nope')).toBe(false);
  });
  test('rejects when unset or header missing', () => {
    expect(service.verifyWebhookAuth(undefined)).toBe(false);
    delete process.env.REVENUECAT_WEBHOOK_AUTH;
    expect(service.verifyWebhookAuth(AUTH)).toBe(false);
  });
});

describe('processWebhookEvent — ledger', () => {
  test('ledgers with the rc: prefixed id and skipDuplicates', async () => {
    await service.processWebhookEvent(rcEvent());
    expect(mockPrisma.subscription_events.createMany).toHaveBeenCalledWith({
      data: [
        expect.objectContaining({
          razorpay_event_id: 'rc:evt_rc_1',
          event_type: 'INITIAL_PURCHASE',
          mac_address: MAC,
        }),
      ],
      skipDuplicates: true,
    });
  });

  test('duplicate event id is a no-op', async () => {
    mockPrisma.subscription_events.createMany.mockResolvedValue({ count: 0 });
    const { outcome } = await service.processWebhookEvent(rcEvent());
    expect(outcome).toBe('duplicate');
    expect(mockPrisma.device_subscriptions.updateMany).not.toHaveBeenCalled();
  });
});

describe('INITIAL_PURCHASE / RENEWAL → active', () => {
  test.each(['INITIAL_PURCHASE', 'RENEWAL'])('%s activates with store period anchors', async (type) => {
    const { outcome } = await service.processWebhookEvent(rcEvent({ type }));
    expect(outcome).toBe('processed');

    // Upsert first: webhook-before-bind still lands a row.
    expect(mockPrisma.device_subscriptions.upsert).toHaveBeenCalledWith(
      expect.objectContaining({ where: { mac_address: MAC } })
    );

    const call = mockPrisma.device_subscriptions.updateMany.mock.calls[0][0];
    expect(call.data).toMatchObject({
      status: 'active',
      current_period_start: new Date(1752700000000),
      current_period_end: new Date(1755378400000),
      store: 'app_store',
      rc_original_transaction_id: 'txn_1',
      plan_id: 2n,
      grace_until: null,
      cancel_at_period_end: false,
    });
    // Forward-only anchor guard present.
    expect(call.where.OR).toEqual([
      { current_period_start: null },
      { current_period_start: { lte: new Date(1752700000000) } },
    ]);
  });

  test('stale event rejected by the guard is ledgered, not applied blind', async () => {
    mockPrisma.device_subscriptions.updateMany.mockResolvedValue({ count: 0 });
    const { outcome } = await service.processWebhookEvent(rcEvent({ type: 'RENEWAL' }));
    expect(outcome).toBe('ledgered');
  });

  test('unknown product_id keeps the existing plan', async () => {
    mockPrisma.subscription_plans.findFirst.mockResolvedValue(null);
    await service.processWebhookEvent(rcEvent());
    const call = mockPrisma.device_subscriptions.updateMany.mock.calls[0][0];
    expect(call.data.plan_id).toBeUndefined();
  });

  test('PLAY_STORE maps to play_store', async () => {
    await service.processWebhookEvent(rcEvent({ store: 'PLAY_STORE' }));
    const call = mockPrisma.device_subscriptions.updateMany.mock.calls[0][0];
    expect(call.data.store).toBe('play_store');
  });

  test('invalid app_user_id (not a MAC) is ledgered only', async () => {
    const { outcome } = await service.processWebhookEvent(rcEvent({ app_user_id: 'anonymous-xyz' }));
    expect(outcome).toBe('ledgered');
    expect(mockPrisma.device_subscriptions.updateMany).not.toHaveBeenCalled();
  });
});

describe('ledger-only default', () => {
  test.each(['BILLING_ISSUE', 'TRANSFER', 'SOME_FUTURE_TYPE'])('%s is ledgered without transition', async (type) => {
    const { outcome } = await service.processWebhookEvent(rcEvent({ type }));
    expect(outcome).toBe('ledgered');
    expect(mockPrisma.device_subscriptions.updateMany).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx jest tests/unit/revenuecat.service.test.js`
Expected: FAIL — `Cannot find module '../../src/services/revenuecat.service'`

- [ ] **Step 3: Implement `src/services/revenuecat.service.js`**

```javascript
/**
 * RevenueCat webhook integration (SUB-15).
 *
 * IAP pivot rails (design doc 2026-07-21): the app buys natively via
 * purchases_flutter with appUserID = device MAC; RevenueCat validates store
 * receipts and delivers ONE normalized webhook here. Same delivery contract
 * defenses as the Razorpay handler (SUB-6): at-least-once → ledger dedupe,
 * unordered → period anchors only ever advance. There is no checkout race at
 * all — no server-side checkout exists on these rails.
 *
 * BILLING_ISSUE / refunds are ledgered only (SUB-7 owns grace).
 * ponytail: no live re-derive on a rejected stale write — a newer anchor is
 * already in place, which IS the derived state; SUB-7's nightly RC
 * reconciliation covers real drift.
 */

const crypto = require('crypto');
const { prisma } = require('../config/database');
const { normalizeMacAddress } = require('../utils/helpers');
const logger = require('../utils/logger');

/** RC store field → our device_subscriptions.store value. */
const STORE_MAP = { APP_STORE: 'app_store', PLAY_STORE: 'play_store' };

/**
 * RC sends the configured value verbatim in the Authorization header.
 * Constant-time compare; false when unset (route maps that to 503 first).
 *
 * @param {string|undefined} headerValue
 * @returns {boolean}
 */
const verifyWebhookAuth = (headerValue) => {
  const secret = process.env.REVENUECAT_WEBHOOK_AUTH;
  if (!secret || !headerValue) return false;
  const provided = Buffer.from(String(headerValue));
  const wanted = Buffer.from(secret);
  return provided.length === wanted.length && crypto.timingSafeEqual(provided, wanted);
};

/** Our plan row id for an RC product id; null when unknown (keep existing). */
const planIdForProduct = async (productId) => {
  if (!productId) return null;
  const plan = await prisma.subscription_plans.findFirst({
    where: { store_product_id: productId },
    select: { id: true },
  });
  return plan?.id ?? null;
};

/**
 * INITIAL_PURCHASE / RENEWAL: the row becomes active with the store's billing
 * period as the bucket anchor. Anchor guard = out-of-order defense.
 * Returns false when the guard rejected the write (stale event).
 */
const applyActiveState = async (normalizedMac, event) => {
  const periodStart = event.purchased_at_ms ? new Date(event.purchased_at_ms) : null;
  const periodEnd = event.expiration_at_ms ? new Date(event.expiration_at_ms) : null;
  const planId = await planIdForProduct(event.product_id);

  const data = {
    status: 'active',
    current_period_start: periodStart,
    current_period_end: periodEnd,
    store: STORE_MAP[event.store] || null,
    rc_original_transaction_id: event.original_transaction_id || undefined,
    grace_until: null,
    cancel_at_period_end: false,
    ...(planId != null ? { plan_id: planId } : {}),
    updated_at: new Date(),
  };

  // Upsert first so a webhook that beats the bind still lands a row.
  await prisma.device_subscriptions.upsert({
    where: { mac_address: normalizedMac },
    create: { mac_address: normalizedMac, ...data },
    update: {},
  });

  const { count } = await prisma.device_subscriptions.updateMany({
    where: {
      mac_address: normalizedMac,
      ...(periodStart
        ? { OR: [{ current_period_start: null }, { current_period_start: { lte: periodStart } }] }
        : {}),
    },
    data,
  });
  return count > 0;
};

/**
 * Process one RC webhook event: ledger it (dupe ⇒ stop), then transition.
 * Ledger key reuses subscription_events.razorpay_event_id with an 'rc:'
 * prefix — one unique column, two rails, zero collision.
 *
 * @param {Object} event - body.event from the RC webhook payload
 * @returns {Promise<{outcome: 'duplicate'|'processed'|'ledgered'}>}
 */
const processWebhookEvent = async (event) => {
  const normalizedMac = normalizeMacAddress(event?.app_user_id) || null;

  const { count } = await prisma.subscription_events.createMany({
    data: [
      {
        razorpay_event_id: `rc:${event.id}`,
        event_type: event?.type || 'unknown',
        mac_address: normalizedMac,
        payload: event,
      },
    ],
    skipDuplicates: true,
  });
  if (count === 0) {
    logger.info(`[REVENUECAT] Duplicate webhook rc:${event.id} — no-op`);
    return { outcome: 'duplicate' };
  }

  let handled = false;
  if (normalizedMac) {
    switch (event?.type) {
      case 'INITIAL_PURCHASE':
      case 'RENEWAL': {
        const applied = await applyActiveState(normalizedMac, event);
        handled = applied;
        logger.info(
          `[REVENUECAT] ${event.type} for ${normalizedMac}: ${applied ? 'applied' : 'stale — anchor guard rejected'}`
        );
        break;
      }

      case 'CANCELLATION':
        // Auto-renew turned off; entitlement runs to period end (spec state machine).
        await prisma.device_subscriptions.updateMany({
          where: { mac_address: normalizedMac },
          data: { cancel_at_period_end: true, updated_at: new Date() },
        });
        handled = true;
        break;

      case 'UNCANCELLATION':
        await prisma.device_subscriptions.updateMany({
          where: { mac_address: normalizedMac },
          data: { cancel_at_period_end: false, updated_at: new Date() },
        });
        handled = true;
        break;

      case 'PRODUCT_CHANGE': {
        // Store-native upgrade/downgrade (SUB-9): swap the plan; anchors move
        // with the next RENEWAL, which encodes the store's effective timing.
        const planId = await planIdForProduct(event.new_product_id || event.product_id);
        if (planId != null) {
          await prisma.device_subscriptions.updateMany({
            where: { mac_address: normalizedMac },
            data: { plan_id: planId, updated_at: new Date() },
          });
          handled = true;
        }
        break;
      }

      case 'EXPIRATION':
        await prisma.device_subscriptions.updateMany({
          where: { mac_address: normalizedMac },
          data: { status: 'lapsed', updated_at: new Date() },
        });
        handled = true;
        break;

      default:
        // BILLING_ISSUE / TRANSFER / future types → ledgered for SUB-7.
        break;
    }
  } else if (event?.app_user_id) {
    logger.warn(`[REVENUECAT] Webhook rc:${event.id} app_user_id "${event.app_user_id}" is not a MAC — ledgered only`);
  }

  await prisma.subscription_events.updateMany({
    where: { razorpay_event_id: `rc:${event.id}` },
    data: { processed_at: new Date() },
  });

  return { outcome: handled ? 'processed' : 'ledgered' };
};

module.exports = { verifyWebhookAuth, processWebhookEvent };
```

- [ ] **Step 4: Run tests to verify pass**

Run: `npx jest tests/unit/revenuecat.service.test.js`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add src/services/revenuecat.service.js tests/unit/revenuecat.service.test.js
git commit -m "feat(subscription): RevenueCat event processing service (SUB-15)"
```

---

### Task 3: CANCELLATION / UNCANCELLATION / PRODUCT_CHANGE / EXPIRATION tests

(Implementation already landed in Task 2's service — this task locks the behavior with tests a reviewer can gate on.)

**Files:**
- Modify: `tests/unit/revenuecat.service.test.js` (append describe block)

**Interfaces:**
- Consumes: `service.processWebhookEvent` and mocks from Task 2.

- [ ] **Step 1: Append tests**

```javascript
describe('lifecycle transitions', () => {
  test('CANCELLATION sets cancel_at_period_end only', async () => {
    const { outcome } = await service.processWebhookEvent(rcEvent({ type: 'CANCELLATION' }));
    expect(outcome).toBe('processed');
    expect(mockPrisma.device_subscriptions.updateMany).toHaveBeenCalledWith({
      where: { mac_address: MAC },
      data: expect.objectContaining({ cancel_at_period_end: true }),
    });
    const data = mockPrisma.device_subscriptions.updateMany.mock.calls[0][0].data;
    expect(data.status).toBeUndefined();
  });

  test('UNCANCELLATION clears cancel_at_period_end', async () => {
    await service.processWebhookEvent(rcEvent({ type: 'UNCANCELLATION' }));
    expect(mockPrisma.device_subscriptions.updateMany).toHaveBeenCalledWith({
      where: { mac_address: MAC },
      data: expect.objectContaining({ cancel_at_period_end: false }),
    });
  });

  test('PRODUCT_CHANGE swaps plan_id by new_product_id', async () => {
    mockPrisma.subscription_plans.findFirst.mockResolvedValue({ id: 3n });
    const { outcome } = await service.processWebhookEvent(
      rcEvent({ type: 'PRODUCT_CHANGE', new_product_id: 'cheeko_premium_monthly' })
    );
    expect(outcome).toBe('processed');
    expect(mockPrisma.subscription_plans.findFirst).toHaveBeenCalledWith({
      where: { store_product_id: 'cheeko_premium_monthly' },
      select: { id: true },
    });
    expect(mockPrisma.device_subscriptions.updateMany).toHaveBeenCalledWith({
      where: { mac_address: MAC },
      data: expect.objectContaining({ plan_id: 3n }),
    });
  });

  test('PRODUCT_CHANGE to an unknown product is ledgered, no swap', async () => {
    mockPrisma.subscription_plans.findFirst.mockResolvedValue(null);
    const { outcome } = await service.processWebhookEvent(
      rcEvent({ type: 'PRODUCT_CHANGE', new_product_id: 'unknown_product' })
    );
    expect(outcome).toBe('ledgered');
    expect(mockPrisma.device_subscriptions.updateMany).not.toHaveBeenCalled();
  });

  test('EXPIRATION lapses the row', async () => {
    await service.processWebhookEvent(rcEvent({ type: 'EXPIRATION' }));
    expect(mockPrisma.device_subscriptions.updateMany).toHaveBeenCalledWith({
      where: { mac_address: MAC },
      data: expect.objectContaining({ status: 'lapsed' }),
    });
  });
});
```

- [ ] **Step 2: Run tests**

Run: `npx jest tests/unit/revenuecat.service.test.js`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add tests/unit/revenuecat.service.test.js
git commit -m "test(subscription): RevenueCat lifecycle transition coverage (SUB-15)"
```

---

### Task 4: Route + mount + integration tests

**Files:**
- Create: `src/routes/revenuecatWebhook.routes.js`
- Modify: `src/app.js:94-96` (mount AFTER `express.json` — RC needs no raw body)
- Test: `tests/integration/revenuecat-webhook.test.js`

**Interfaces:**
- Consumes: `verifyWebhookAuth(headerValue)`, `processWebhookEvent(event)` from Task 2.
- Produces: `POST /webhooks/revenuecat` — 503 secret unset, 401 bad/missing Authorization, 400 missing `event.id`, 200 `{code: 0, msg: outcome}`.

- [ ] **Step 1: Write failing integration tests**

`tests/integration/revenuecat-webhook.test.js`:

```javascript
/**
 * POST /webhooks/revenuecat integration tests (SUB-15).
 *
 * Runs without a live database (tests/setup.js) — asserts the gate in front
 * of the ledger: Authorization compare before anything is written. Transition
 * logic is unit-tested in tests/unit/revenuecat.service.test.js.
 */

const { request, app } = require('../setup');

const AUTH = 'rc-integration-secret';

describe('POST /webhooks/revenuecat', () => {
  const original = process.env.REVENUECAT_WEBHOOK_AUTH;
  afterEach(() => {
    if (original === undefined) delete process.env.REVENUECAT_WEBHOOK_AUTH;
    else process.env.REVENUECAT_WEBHOOK_AUTH = original;
  });

  it('returns 503 when the auth secret is not configured', async () => {
    delete process.env.REVENUECAT_WEBHOOK_AUTH;
    const res = await request(app)
      .post('/webhooks/revenuecat')
      .send({ api_version: '1.0', event: { id: 'e1', type: 'TEST' } });
    expect(res.statusCode).toBe(503);
  });

  it('returns 401 on a wrong Authorization header', async () => {
    process.env.REVENUECAT_WEBHOOK_AUTH = AUTH;
    const res = await request(app)
      .post('/webhooks/revenuecat')
      .set('Authorization', 'wrong')
      .send({ api_version: '1.0', event: { id: 'e1', type: 'TEST' } });
    expect(res.statusCode).toBe(401);
  });

  it('returns 401 when the Authorization header is missing', async () => {
    process.env.REVENUECAT_WEBHOOK_AUTH = AUTH;
    const res = await request(app)
      .post('/webhooks/revenuecat')
      .send({ api_version: '1.0', event: { id: 'e1', type: 'TEST' } });
    expect(res.statusCode).toBe(401);
  });

  it('returns 400 on an authorized body without an event id', async () => {
    process.env.REVENUECAT_WEBHOOK_AUTH = AUTH;
    const res = await request(app)
      .post('/webhooks/revenuecat')
      .set('Authorization', AUTH)
      .send({ api_version: '1.0', event: { type: 'TEST' } });
    expect(res.statusCode).toBe(400);
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx jest tests/integration/revenuecat-webhook.test.js`
Expected: FAIL (404s — route not mounted yet)

- [ ] **Step 3: Implement `src/routes/revenuecatWebhook.routes.js`**

```javascript
/**
 * POST /webhooks/revenuecat (SUB-15)
 *
 * RevenueCat retries failed deliveries with backoff, so config errors return
 * 503 (retryable) and only a bad Authorization value gets the terminal 401.
 * JSON body — no raw-body HMAC on these rails, mounts after the JSON parser.
 */

const express = require('express');
const asyncHandler = require('express-async-handler');
const revenuecatService = require('../services/revenuecat.service');
const logger = require('../utils/logger');

const router = express.Router();

router.post(
  '/',
  asyncHandler(async (req, res) => {
    if (!process.env.REVENUECAT_WEBHOOK_AUTH) {
      logger.error('[REVENUECAT] REVENUECAT_WEBHOOK_AUTH not set — webhook rejected');
      return res.status(503).json({ code: 503, msg: 'Webhook auth not configured' });
    }

    if (!revenuecatService.verifyWebhookAuth(req.headers.authorization)) {
      logger.warn('[REVENUECAT] Webhook Authorization mismatch');
      return res.status(401).json({ code: 401, msg: 'Invalid authorization' });
    }

    const event = req.body?.event;
    if (!event?.id) {
      // Authorized but unidentifiable — no idempotency key.
      return res.status(400).json({ code: 400, msg: 'Missing event.id' });
    }

    const { outcome } = await revenuecatService.processWebhookEvent(event);
    return res.status(200).json({ code: 0, msg: outcome });
  })
);

module.exports = router;
```

- [ ] **Step 4: Mount in `src/app.js`**

Directly AFTER the `app.use(express.json({ limit: '10mb' }));` line (which follows the Razorpay mount at line 94):

```javascript
// RevenueCat webhook (SUB-15, IAP rails) — plain JSON, static-header auth.
app.use('/webhooks/revenuecat', require('./routes/revenuecatWebhook.routes'));
```

- [ ] **Step 5: Run integration tests**

Run: `npx jest tests/integration/revenuecat-webhook.test.js`
Expected: PASS (4 tests)

- [ ] **Step 6: Commit**

```bash
git add src/routes/revenuecatWebhook.routes.js src/app.js tests/integration/revenuecat-webhook.test.js
git commit -m "feat(subscription): /webhooks/revenuecat endpoint (SUB-15)"
```

---

### Task 5: Full suite + ticket close

**Files:**
- Modify: `D:\picoclaw\docs\issues\subscription\015-revenuecat-backend.md` (status + resolution)

- [ ] **Step 1: Full test suite**

Run (in `D:\cheeko-backend\main\manager-api-node`): `npm test`
Expected: all suites pass, including the 24 SUB-6 tests and the new SUB-15 ones. If it hangs: check for stale jest `node.exe` processes and kill them, then re-run.

- [ ] **Step 2: Walk acceptance criteria in the ticket**

Each criterion in `015-revenuecat-backend.md` maps to a test written above (sandbox `INITIAL_PURCHASE` → activation test; replay → duplicate test; 401/503 → integration tests; RENEWAL anchors + stale guard → anchor tests; PRODUCT_CHANGE → swap test; unknown-type → ledger-only test). Tick the boxes that have a passing test; the literal "sandbox" delivery against a deployed endpoint stays unticked until SUB-17's webhook is pointed at a live environment — note that in the resolution, do not fake it.

- [ ] **Step 3: Close the ticket**

Set `status: closed`, append `## Resolution` (what shipped, commit hashes, the sandbox-delivery caveat). Commit in `D:\picoclaw`:

```bash
git add docs/issues/subscription/015-revenuecat-backend.md
git commit -m "docs(subscription): close SUB-15 (RevenueCat webhook backend)"
```

---

## Self-Review Notes

- Spec coverage: ticket criteria ↔ tasks mapped in Task 5 Step 2; `store_product_id` seed matches SUB-17's product-id checklist (`cheeko_<tier>_monthly`).
- Ledger reuses the existing unique column with an `rc:` prefix — deliberate (one rails-agnostic ledger; renaming the column would churn SUB-6 code for nothing).
- No live re-derive on stale writes (Razorpay's `deriveFromLiveState` equivalent) — deliberate ponytail ceiling, documented in the service header; SUB-7's reconciliation owns drift.
- Types consistent: `verifyWebhookAuth(headerValue)` and `processWebhookEvent(event)` used identically in Tasks 2 and 4.
