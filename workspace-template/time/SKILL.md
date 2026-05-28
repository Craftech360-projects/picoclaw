---
name: time
description: Get deterministic date and time information with timezone support.
---

# Time

Use the native `get_time_date` tool for all date/time questions.

## Rules

- Prefer the user's timezone when it is known.
- If a timezone is not provided, call `get_time_date` without timezone and clearly state which timezone was used.
- For relative references like today, tomorrow, and yesterday, include the exact date.
