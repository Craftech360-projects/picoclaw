#!/usr/bin/env node
// One-shot local startup for picoclaw-livekit (Go worker).
//   node start-picoclaw.js              # docker -> livekit -> build -> launch worker
//   node start-picoclaw.js --no-build   # skip the Go build, just launch the existing .exe
//   node start-picoclaw.js --self-test
//
// Notes:
// - The worker connects OUT to LiveKit + the cheeko Manager API (:8002). It binds no
//   public port, so there's nothing to free. Make sure the cheeko backend is running.
// - OPENAI_BASE_URL in .env points at a SEPARATE machine (speaches PC), so we don't touch IPs here.

const fs = require('fs');
const path = require('path');
const { execSync, spawn } = require('child_process');

const ROOT = __dirname;
const NEEDED = [{ label: 'LiveKit', match: 'livekit' }]; // worker needs the LiveKit server container

function dockerRunning() {
  try { execSync('docker info', { stdio: 'ignore' }); return true; } catch { return false; }
}

function waitForEnter(promptText) {
  process.stdout.write(promptText);
  const buf = Buffer.alloc(256);
  const n = fs.readSync(0, buf, 0, buf.length, null);
  return buf.toString('utf8', 0, n).trim().toLowerCase();
}

function ensureDocker() {
  console.log('\nChecking Docker...');
  if (dockerRunning()) { console.log('Docker is already running.'); return true; }
  console.log('Docker is NOT running. Start Docker Desktop and wait for the whale icon to go steady.');
  while (true) {
    const ans = waitForEnter('When ready, press Enter to re-check (or type "skip" to continue): ');
    if (ans === 'skip') { console.log('Skipped — continuing without Docker.'); return false; }
    if (dockerRunning()) { console.log('Docker is running ✓'); return true; }
    console.log('Still not ready. Give it a bit more time, then try again.');
  }
}

function containerNames(allFlag) {
  const out = execSync(`docker ps ${allFlag ? '-a ' : ''}--format "{{.Names}}"`, { encoding: 'utf8' });
  return out.split('\n').map((s) => s.trim()).filter(Boolean);
}

function ensureContainers() {
  console.log('\nChecking containers...');
  const running = containerNames(false), all = containerNames(true);
  for (const { label, match } of NEEDED) {
    const live = running.find((n) => n.toLowerCase().includes(match));
    if (live) { console.log(`${label}: running ✓ (${live})`); continue; }
    const stopped = all.find((n) => n.toLowerCase().includes(match));
    if (stopped) {
      process.stdout.write(`${label}: starting ${stopped}... `);
      try { execSync(`docker start ${stopped}`, { stdio: 'ignore' }); console.log('started ✓'); }
      catch { console.log(`failed — start it manually: docker start ${stopped}`); }
    } else {
      console.log(`${label}: no container found — start the cheeko LiveKit container first.`);
    }
  }
}

function checkManagerApi() {
  // worker uses the Manager API for session persistence; warn (don't fail) if it's down.
  try {
    execSync('curl -s -o NUL -w "%{http_code}" http://127.0.0.1:8002/toy/health', { stdio: 'ignore', timeout: 4000 });
    console.log('Manager API (:8002): reachable ✓');
  } catch {
    console.log('Manager API (:8002): not reachable — start the cheeko backend (set-local-ip.js).');
  }
}

function build() {
  console.log('\nBuilding picoclaw-livekit (go build via scripts/build-livekit.ps1)...');
  execSync('powershell -ExecutionPolicy Bypass -File scripts\\build-livekit.ps1', { cwd: ROOT, stdio: 'inherit' });
}

const AGENT_NAME = 'cheeko-agent';

function launchWorker() {
  const exe = path.join(ROOT, 'picoclaw-livekit.exe');
  if (!fs.existsSync(exe)) throw new Error('picoclaw-livekit.exe not found — run without --no-build first.');
  console.log('\nLaunching worker in its own terminal...');
  const cmd = `picoclaw-livekit.exe -agent-name ${AGENT_NAME}`;
  spawn(`start "picoclaw-livekit" /D "${ROOT}" cmd /k "${cmd}"`,
    { shell: true, detached: true, stdio: 'ignore' }).unref();
  console.log(`  ${cmd}`);
}

function main() {
  const args = process.argv.slice(2);
  if (args.includes('--self-test')) {
    console.assert(NEEDED.some((n) => n.match === 'livekit'), 'livekit needed');
    console.log('self-test OK');
    return;
  }
  if (ensureDocker()) ensureContainers();
  checkManagerApi();
  if (!args.includes('--no-build')) build();
  launchWorker();
}

main();
