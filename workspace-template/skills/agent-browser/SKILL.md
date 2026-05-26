---
name: agent-browser
description: Web research skill for voice runtime using native tools only.
---

# Agent Browser (Voice Runtime)

Use native tools only:
- `web_search` to find relevant sources
- `web_fetch` to open and read source pages

## Rules

- Do not use browser automation CLIs, shell commands, or external executables.
- Do not claim you can control a browser.
- For time-sensitive questions (latest/today/scores/news), run `web_search` first and then `web_fetch` trusted result pages.
- If results conflict, say so clearly and report what each source says.
- If you cannot verify, explicitly say you could not verify.

## Response Style

- Keep answers short and child-friendly.
- Mention source names naturally in the response when summarizing web results.