# LiveKit Worker Skill Sync

LiveKit workers need the same active skills no matter which worker accepts a
device reconnect. Skills remain file-based folders with `SKILL.md` files.

## Docker Workers

Docker images bundle release skills at:

```text
/opt/picoclaw/skills
```

At container startup, `docker/entrypoint.sh` syncs that bundle into:

```text
${PICOCLAW_HOME:-$HOME/.picoclaw}/workspace/skills
```

You can override the image bundle by mounting the same read-only folder into
every worker:

```yaml
volumes:
  - /srv/picoclaw/skills:/opt/picoclaw/skills:ro
```

## PM2 Or Direct Windows Workers

When running `picoclaw-livekit.exe` directly, the Docker entrypoint does not
run. The LiveKit worker hydrates every device workspace from these skill
sources, in priority order:

```text
~/.picoclaw/workspace/skills
~/.picoclaw/skills
~/.picoclaw/picoclaw/skills
PICOCLAW_BUILTIN_SKILLS, if set
<current-working-directory>/workspace/skills
<current-working-directory>/skills
```

For PM2 on Windows, run from the repo root or set `PICOCLAW_BUILTIN_SKILLS`:

```powershell
$env:PATH = "D:\picoclaw;$env:PATH"
$env:PICOCLAW_BUILTIN_SKILLS = "D:\picoclaw\workspace\skills"
$env:PICOCLAW_LIVEKIT_SKILLS = "weather,agent-browser"
pm2 start .\picoclaw-livekit.exe --name cheeko-agent --cwd D:\picoclaw -- --agent-name cheeko-agent --log-level debug
```

`D:\picoclaw` must be on `PATH` on Windows so `ten_vad.dll` can be loaded.

## Device Workspace Hydration

When a device joins a room, the worker creates or updates:

```text
workspace-device-<normalized-mac>/skills
```

from the resolved skill sources. Lower-priority sources are copied first, then
higher-priority sources override them. This lets a local workspace skill replace
a bundled skill with the same name.

## Active Skills

Installed skills are available in the workspace. Active LiveKit skills are
configured separately:

```text
PICOCLAW_LIVEKIT_SKILLS=weather,agent-browser
```

or:

```json
{
  "livekit_service": {
    "skills": ["weather", "agent-browser"]
  }
}
```

## Verification

Check base skills:

```powershell
Test-Path "$env:USERPROFILE\.picoclaw\workspace\skills\weather\SKILL.md"
Test-Path "$env:USERPROFILE\.picoclaw\workspace\skills\agent-browser\SKILL.md"
```

Check a hydrated device workspace:

```powershell
Test-Path "$env:USERPROFILE\.picoclaw\workspace-device-00163eacb538\skills\weather\SKILL.md"
Test-Path "$env:USERPROFILE\.picoclaw\workspace-device-00163eacb538\skills\agent-browser\SKILL.md"
```

Expected logs include:

```text
Hydrated LiveKit workspace skeleton ... skills_copied=...
Validated LiveKit active skills ... missing_skills=[]
```

If `missing_skills` is not empty, the worker can see the active skill names in
config but could not find matching `SKILL.md` files in the hydrated device
workspace.
