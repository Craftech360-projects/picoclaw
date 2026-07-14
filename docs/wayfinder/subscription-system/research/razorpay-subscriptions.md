# Razorpay Subscriptions — Research Findings

- **Date:** 2026-07-14
- **Resolves:** Wayfinder ticket 006
- **Sources:** Razorpay official documentation (razorpay.com/docs) only. Anything not confirmable from official docs is flagged **UNVERIFIED**.

---

## 1. Subscriptions API shape

**Plan object** ([Create a Plan](https://razorpay.com/docs/api/payments/subscriptions/create-plan/))
- `POST /v1/plans` with: `period` (`daily` | `weekly` | `monthly` | `quarterly` | `yearly`), `interval` (integer multiplier; daily requires interval >= 7), `item.name`, `item.amount` (paise, min 100 for INR), `item.currency`, optional `notes` (max 15 pairs).
- **Monthly plan:** `period: "monthly", interval: 1`. **Annual plan:** `period: "yearly", interval: 1`. Monthly and annual tiers are therefore **separate plan objects**; a subscription points at exactly one plan.
- Response: `id` (`plan_...`), `entity: "plan"`, echoed `period`/`interval`, nested `item` with auto-generated item id, `notes`, `created_at`.

**Subscription object** ([Create a Subscription](https://razorpay.com/docs/api/payments/subscriptions/create-subscription/), [Subscriptions APIs](https://razorpay.com/docs/api/payments/subscriptions/))
- `POST /v1/subscriptions` with required `plan_id`, `total_count` (number of billing cycles); optional `quantity`, `start_at` (Unix ts), `expire_by` (deadline for the customer to complete the authorization payment), `customer_notify` (default true — Razorpay sends customer comms), `addons` (upfront charges collected at authorization), `offer_id`, `notes`.
- Entity fields: `id`, `status` (created / authenticated / active / pending / halted / cancelled / completed / expired, plus paused/resumed states), `customer_id`, `current_start`, `current_end`, `charge_at` (next charge ts), `auth_attempts`, `paid_count`, `remaining_count`.

**Customer object** ([Customers API](https://razorpay.com/docs/api/customers/), [How Subscriptions Work](https://razorpay.com/docs/payments/subscriptions/workflow/))
- Fields: `id`, `name`, `email`, `contact`, `gstin`, `notes`; `fail_existing` parameter controls duplicate-creation behavior.
- For Subscriptions you do **not** pre-create a customer: "Razorpay automatically creates a customer when the authentication payment is made" — `customer_id` is populated on the subscription after the customer completes the authorization transaction.

**Multiple simultaneous subscriptions per customer:** **UNVERIFIED.** Official docs show creating multiple subscriptions against the same plan and document no restriction on a customer holding several subscriptions, but no doc page explicitly states "one customer can hold N simultaneous subscriptions." Practically, each subscription carries its own mandate/token and its own `customer_id` linkage; plan for one mandate per subscription. Confirm with Razorpay support before relying on it for plan-switching design.

---

## 2. UPI Autopay mandates

Sources: [UPI Autopay (S2S recurring)](https://razorpay.com/docs/payments/payment-gateway/s2s-integration/recurring-payments/upi/), [Recurring UPI](https://razorpay.com/docs/payments/recurring-payments/upi/), [Subscriptions FAQs](https://razorpay.com/docs/payments/subscriptions/faqs/).

**Amount limits / AFA thresholds**
- General merchants: "any subsequent debit above ₹15,000 requires the customer to approve the transaction via UPI PIN" (AFA). Debits **up to ₹15,000 execute without customer intervention**.
- Exempt categories (MCCs 6211, 6300, 7322, 6529, 5960 — lending/investment): AFA-free up to ₹1,00,000. A consumer-app subscription business will **not** qualify for these MCCs.
- Mandate minimum is ₹1; if a debit exceeds the mandate's `max_amount`, "the payment will fail."
- **Implication for this project:** ₹199–999/month is comfortably inside the ₹15,000 silent-debit cap. Annual charges of ₹2,000–10,000 are also under ₹15,000, so a **yearly UPI Autopay mandate works without per-charge PIN approval**. UNVERIFIED: the exact `max_amount` Razorpay sets on the mandate when a Subscription (as opposed to a raw S2S mandate) is created — docs do not state whether it equals the plan amount or a buffer above it.

**Auth flow**
- Customer explicitly initiates mandate registration via Razorpay Standard Checkout (pass `subscription_id`) or a Subscription/Registration Link; approves in their UPI app with PIN; a token is generated on successful capture. Subsequent debits then run against the mandate without PIN re-entry.
- Regulatory requirement: a **pre-debit notification (PDN) must reach the customer at least 24 hours before every debit** — Razorpay handles this; it constrains how fast you can charge after `charge_at`.

**Mandate revocation / pause by the user**
- "Your customer can cancel only those subscriptions authorised via UPI from their UPI app" — cancellation kills future auto-debits and fires the `subscription.cancelled` webhook; a cancelled subscription **cannot be reactivated** (new subscription + new mandate required).
- Customer-initiated **pause**: "For UPI Subscriptions, you cannot resume a Subscription paused by your customer. If your customer pauses a Subscription, only they can resume it."

---

## 3. Webhook events

Sources: [Subscription webhook payloads](https://razorpay.com/docs/webhooks/payloads/subscriptions/), [Webhooks](https://razorpay.com/docs/webhooks/), [Webhook best practices](https://razorpay.com/docs/webhooks/best-practices/), [Validate and test webhooks](https://razorpay.com/docs/webhooks/validate-test/).

**Full subscription lifecycle event list**
| Event | Trigger |
|---|---|
| `subscription.authenticated` | Authorization transaction completed |
| `subscription.activated` | Subscription moves to active |
| `subscription.charged` | Recurring charge succeeded |
| `subscription.pending` | Charge failed; Razorpay retrying |
| `subscription.halted` | All retries exhausted |
| `subscription.paused` | Subscription paused |
| `subscription.resumed` | Paused subscription resumed |
| `subscription.updated` | Subscription modified (plan change etc.) |
| `subscription.cancelled` | Terminated (merchant, API, or customer via UPI app) |
| `subscription.completed` | All `total_count` cycles paid |

- There is **no dedicated `subscription.payment_failed` event**; failures surface as `subscription.pending` then `subscription.halted`. Payloads carry the subscription entity, plus a payment entity "if a payment attempt was made before the event was triggered." (You can additionally subscribe to generic `payment.failed`.)

**Delivery guarantees & retries**
- **At-least-once** semantics — duplicates expected; dedupe on the unique `x-razorpay-event-id` header.
- Failure = any non-2xx response, or no response within **5 seconds**.
- Retries with **exponential backoff for 24 hours** from event creation; if failing the entire window, the **webhook is auto-disabled** (email alert sent; manual re-enable from Dashboard).
- Ordering **not guaranteed**: "you may not always receive the webhooks in order."

**Signature verification**
- Header `X-Razorpay-Signature` = HMAC-SHA256 over the **raw request body**, keyed with your webhook secret ("Do not parse or cast the webhook request body"). SDK helpers exist (e.g., `Utils.verifyWebhookSignature()`).

---

## 4. Dunning / retry on failed charges

Sources: [Payment Retries](https://razorpay.com/docs/payments/subscriptions/payment-retries/), [Subscription States](https://razorpay.com/docs/payments/subscriptions/states/).

- On a failed auto-charge the subscription enters `pending`; "We continue to retry the payment while it is in this state." Cards are retried "on the following day" (daily cadence). For eMandate/UPI, a retry happens only after confirmation/rejection of the previous attempt, "may take more than 24 hours," and bank holidays shift charges by T-1/T-3 days.
- After retries are exhausted → `halted`: invoices keep generating each cycle but **no auto-charge is attempted**. Recovery: customer authenticates a new payment method, or unpaid `issued` invoices are charged manually (manual attempts don't count against retry limits).
- **UNVERIFIED — exact retry count/window:** docs do not publish the number of attempts or total days before `pending` → `halted`. The retry schedule is **not merchant-configurable** per the docs.
- Razorpay emails the customer on failure with a link to update card details (when `customer_notify` is on).
- **Grace-period conclusion:** Razorpay's dunning is opaque and fixed; a product-level 3-day grace period **must be implemented merchant-side** — key access off `subscription.pending` (enter grace) / `subscription.charged` (restore) / `subscription.halted`+timer (revoke). Do not assume Razorpay's retry window equals 3 days.

---

## 5. Plan changes (upgrade/downgrade), proration, pause/resume

Sources: [Update Subscription](https://razorpay.com/docs/api/payments/subscriptions/update-subscription/), [Pause](https://razorpay.com/docs/api/payments/subscriptions/pause-subscription/), [Cancel](https://razorpay.com/docs/api/payments/subscriptions/cancel-subscription/), [Subscriptions FAQs](https://razorpay.com/docs/payments/subscriptions/faqs/).

- **Native update exists:** `PATCH /v1/subscriptions/:id` with `plan_id` (new plan), `quantity`, `remaining_count`, `start_at`, `offer_id`, `schedule_change_at` (`"now"` default — immediate; `"cycle_end"` — at end of current cycle), `customer_notify`.
- **Critical restriction:** "Subscriptions cannot be updated when payment mode is UPI" (400 error); eMandate subscriptions also cannot be updated. **Plan upgrade/downgrade via API works only for card-authorized subscriptions.** For UPI Autopay subscribers, plan changes require **cancel + create a new subscription (new mandate authorization by the customer)**.
- Update allowed only in `authenticated` or `active` states; subscriptions with active offers can only be downgraded at cycle end; concurrent update ops rejected.
- **Proration: UNVERIFIED / not documented.** The Update API docs describe no proration or credit mechanism for mid-cycle changes with `schedule_change_at: "now"`. Assume no automatic proration; handle credits merchant-side or switch at `cycle_end`.
- **Pause/resume:** `POST /v1/subscriptions/:id/pause` (`pause_at: "now"`), only from `active` state (pausing an `authenticated` subscription cancels it); resume via `POST /v1/subscriptions/:id/resume`. Feature must be enabled on the account ("pause feature not enabled" is a documented 400). Customer-paused UPI subscriptions cannot be merchant-resumed.
- **Cancel:** `POST /v1/subscriptions/:id/cancel`, `cancel_at_cycle_end: true|false` (default immediate). Allowed from created/authenticated/active/pending/halted. "Once cancelled, you cannot renew or reactivate it."

---

## 6. Trial support

Sources: [Create a Subscription](https://razorpay.com/docs/api/payments/subscriptions/create-subscription/), [How Subscriptions Work](https://razorpay.com/docs/payments/subscriptions/workflow/), [Test Subscriptions](https://razorpay.com/docs/payments/subscriptions/test/).

- **No `trial_days` parameter.** Trials are modeled by passing a **future `start_at`** at creation: "To create a trial period for your customers, provide a future start date when creating the Subscription" — billing begins at `start_at`, the interim is the free trial.
- **Payment method capture is required upfront.** The customer must complete an **authentication transaction** at signup. For a future-dated subscription this is a **token amount (₹5 in docs examples) that is auto-refunded**; for an immediately-starting subscription it's the full plan amount (or plan + `addons` upfront charges, not refunded). "The authorisation payment used to validate a customer's card is auto-refunded"; all other subscription payments are auto-captured.
- If the customer never completes authentication before `start_at`, the subscription moves to `expired` and cannot be reused.
- **No card-free trial is possible inside Razorpay Subscriptions** — a mandate/token is created at authentication. Truly card-free trials must live in your app layer (create the subscription only when the trial ends).

---

## 7. GST / invoicing

Sources: [How Subscriptions Work](https://razorpay.com/docs/payments/subscriptions/workflow/), [Fetch invoices for a subscription](https://razorpay.com/docs/api/payments/subscriptions/fetch-invoices/), [Invoices](https://razorpay.com/docs/payments/invoices/).

- Razorpay **auto-generates an invoice entity per billing cycle**: "An invoice is generated at the beginning of each billing cycle"; it moves to `paid` on successful charge and an email notification goes out. Fetch via `GET /v1/invoices?subscription_id=:sub_id`.
- Invoice entity carries `amount`, `tax_amount`, `gross_amount`, `invoice_number` (merchant-assigned, may be null), `billing_start`/`billing_end`, statuses (`draft`, `issued`, `partially_paid`, `paid`, `expired`, `cancelled`, `deleted`), and `customer_details` including a `gstin` field (null unless configured).
- **UNVERIFIED / merchant obligation:** the docs do **not** state that auto-generated subscription invoices are GST-compliant tax invoices issued on the merchant's behalf. Razorpay's separate Invoices product supports building GST-compliant invoices, but for Subscriptions treat the auto-invoice as a **payment/billing record**; the **merchant must issue its own GST tax invoice** (with its GSTIN, SAC code, CGST/SGST/IGST breakup) for each charge. Note also that Razorpay charges GST on its own fees to the merchant — unrelated to customer-facing invoices.

---

## 8. Test mode / sandbox

Sources: [Test Subscriptions](https://razorpay.com/docs/payments/subscriptions/test/), [Test cards](https://razorpay.com/docs/payments/payments/test-card-details/), [Test UPI](https://razorpay.com/docs/payments/payments/test-upi-details/), [Validate and test webhooks](https://razorpay.com/docs/webhooks/validate-test/).

- Full lifecycle is testable with test-mode API keys: create plan → create subscription → authenticate via checkout with test cards (any future expiry / any CVV) or test UPI (`success@razorpay`).
- **"Charge this now" button** on the test Dashboard simulates a due charge immediately — no waiting for the billing date — and lets you **choose success or failure**: success fires `subscription.charged`; failure moves the subscription to `pending` and fires `subscription.pending`; exhausted retries produce `subscription.halted`. So the pending/halted dunning path **is simulatable**.
- Webhooks fire from test-mode transactions with **payloads identical in structure to live mode**; use default OTP `754081` when creating/editing webhooks in test mode.
- Documented limitations: subscription **updates can't be tested after making test charges beyond authentication**; test-mode card tokens are valid **only 3 days**, limiting subsequent-debit test windows; manual invoice charges don't count against retry limits.

---

## Summary of UNVERIFIED items

1. Explicit statement that one customer can hold multiple simultaneous subscriptions (implied, never stated).
2. Exact retry count / total dunning window before `pending` → `halted`.
3. Proration behavior on mid-cycle plan change (no mention — assume none).
4. The `max_amount` Razorpay sets on the UPI mandate for a Subscriptions-product subscription.
5. GST-compliance status of auto-generated subscription invoices (assume merchant must issue own tax invoice).
