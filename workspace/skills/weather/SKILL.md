---
name: weather
description: Weather skill for voice runtime using native tool get_weather.
---

# Weather (Voice Runtime)

Use `get_weather` as the first and default tool for weather.

## Rules

- Do not use curl, shell commands, or external weather CLIs.
- Use the location provided by the user directly; if ambiguous, ask one short clarification.
- In answers, include:
  - resolved location
  - local observation time
  - temperature and condition
- If tool output is unclear or missing, say you could not verify.

## Response Style

- Keep it brief and child-friendly.
- Avoid raw JSON in spoken responses.