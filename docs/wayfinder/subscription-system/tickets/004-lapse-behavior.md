---
id: 4
title: Lapse behavior & grace period
type: wayfinder:grilling
status: closed
assignee: rahul
blocked-by: []
---

## Question

What does the toy do when the device has no active subscription (trial over / lapsed / cancelled), and how much grace follows a failed renewal payment?

## Resolution (2026-07-14, charting session)

- **Gate AI only, keep the rest.** AI conversation and AI imagine refuse at session start — gateway skips LiveKit dispatch and plays a kid-friendly notice. RFID card content, downloaded music/stories, and device playback keep working. The toy a parent bought never feels bricked.
- **3 days grace** after a failed renewal: Razorpay retries the mandate, parent gets push + notification to fix payment, AI keeps working. (~₹15 worst-case COGS per lapsed device — cheap goodwill.)
- The exact kid-facing gated experience (what the toy says, in which language, pre-recorded vs generated) is a separate decision: [Kid-facing lapse experience](009-kid-facing-lapse-experience.md).
