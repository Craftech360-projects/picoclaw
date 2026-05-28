cd D:\picoclaw

# stop old processes (safe if none are running)
Get-Process picoclaw-livekit,go -ErrorAction SilentlyContinue | Stop-Process -Force

# build
go build -o .\bin\picoclaw-livekit.exe .\cmd\picoclaw-livekit

# run
.\bin\picoclaw-livekit.exe -agent-name cheeko-agent -config "C:\Users\rahul\.picoclaw\config.json" -log-level info

---

# Linux (this instance)
cd /root/picoclaw

# stop old processes (safe if none are running)
pkill -f picoclaw-livekit || true
pkill -f 'go build.*picoclaw-livekit' || true

# build
mkdir -p ./bin
go build -o ./bin/picoclaw-livekit ./cmd/picoclaw-livekit

# run
./bin/picoclaw-livekit --agent-name cheeko-agent --config /root/.picoclaw/config.json --log-level info

# run with PM2 (recommended)
pm2 delete picoclaw-livekit || true
pm2 start /root/picoclaw/build/picoclaw-livekit --name picoclaw-livekit --cwd /root/picoclaw -- --agent-name cheeko-agent --config /root/.picoclaw/config.json --log-level debug

# check status/logs
pm2 list
pm2 logs picoclaw-livekit --lines 50

# restart and persist across reboot
pm2 restart picoclaw-livekit
pm2 save



# single command

cd /root/picoclaw
make build-livekit && pm2 restart picoclaw-livekit --update-env && sleep 3 && pm2 list && echo '---' && pm2 logs picoclaw-livekit --lines 20 --nostream