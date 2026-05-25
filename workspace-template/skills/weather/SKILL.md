---
name: weather
description: Get current weather and forecasts with verified location matching.
homepage: https://open-meteo.com/en/docs
---

# Weather

Use the native `get_weather` tool first. It already uses Open-Meteo first and falls back to wttr.in when needed.

## Accuracy Rules

- Always restate the matched location, region/country, and observation time in the final answer.
- Do not trust ambiguous place names blindly. If multiple plausible matches exist, ask a short clarification.
- Prefer metric unless the user asks for imperial units.
- If the native tool fails, say verification failed instead of guessing.
