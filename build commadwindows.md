# Build & run — picoclaw-livekit agent

The `-config` path is the key difference between OSes. On Windows the config
lives in the repo root (`D:\picoclaw\config.json`); on the Linux box it's at
`/root/.picoclaw/config.json`. Using the wrong path silently falls back to an
empty default and fails with `livekit_service.server_url is required`.

Config can also come from environment variables (see `.env` / `.security.yml`)
instead of the JSON file, but `server_url` must be provided one way or another.

---

## Windows (PowerShell)

```powershell
cd D:\picoclaw

# stop old processes (safe if none are running)
Get-Process picoclaw-livekit,go -ErrorAction SilentlyContinue | Stop-Process -Force

# build  (needs a C compiler on PATH for cgo / TEN VAD — e.g. MSYS2 mingw gcc)
go build -o .\bin\picoclaw-livekit.exe .\cmd\picoclaw-livekit

# run  (config is in the repo root: D:\picoclaw\config.json)
.\bin\picoclaw-livekit.exe -agent-name cheeko-agent -config .\config.json -log-level info
```

If `go build` fails with a cgo error, add MSYS2 mingw to PATH for the session:

```powershell
$env:Path = "C:\msys64\mingw64\bin;$env:Path"; $env:CC = "gcc"; $env:CGO_ENABLED = "1"
```

---

## Linux

```bash
cd /root/picoclaw

# stop old processes (safe if none are running)
pkill -f picoclaw-livekit || true
pkill -f 'go build.*picoclaw-livekit' || true

# build
mkdir -p ./bin
go build -o ./bin/picoclaw-livekit ./cmd/picoclaw-livekit

# run  (config at /root/.picoclaw/config.json)
./bin/picoclaw-livekit --agent-name cheeko-agent --config /root/.picoclaw/config.json --log-level info
```

### Linux — PM2 (recommended for servers)

```bash
pm2 delete picoclaw-livekit || true
pm2 start /root/picoclaw/build/picoclaw-livekit --name picoclaw-livekit --cwd /root/picoclaw -- --agent-name cheeko-agent --config /root/.picoclaw/config.json --log-level debug

# check status/logs
pm2 list
pm2 logs picoclaw-livekit --lines 50

# restart and persist across reboot
pm2 restart picoclaw-livekit
pm2 save
```

### Linux — single command

```bash
cd /root/picoclaw
make build-livekit && pm2 restart picoclaw-livekit --update-env && sleep 3 && pm2 list && echo '---' && pm2 logs picoclaw-livekit --lines 20 --nostream
```
