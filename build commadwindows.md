cd D:\picoclaw

# stop old processes (safe if none are running)
Get-Process picoclaw-livekit,go -ErrorAction SilentlyContinue | Stop-Process -Force

# build
go build -o .\bin\picoclaw-livekit.exe .\cmd\picoclaw-livekit

# run
.\bin\picoclaw-livekit.exe -agent-name cheeko-agent -config "C:\Users\rahul\.picoclaw\config.json" -log-level info

