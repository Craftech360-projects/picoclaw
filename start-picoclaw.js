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

// Local Ollama (the worker talks to it for the LLM). Keep in sync with config api_base.
const OLLAMA_URL = 'http://localhost:11434';
const OLLAMA_MODEL = 'gemma3:4b';
const SPEACHES_CONTAINER = 'speaches';
const SPEACHES_COMPOSE = path.join(ROOT, 'speaches', 'docker-compose.yml');

function dockerRunning() {
  try { execSync('docker info', { stdio: 'ignore' }); return true; } catch { return false; }
}

function sleepSec(s) { Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, s * 1000); }

function waitForEnter(promptText) {
  process.stdout.write(promptText);
  const buf = Buffer.alloc(256);
  const n = fs.readSync(0, buf, 0, buf.length, null);
  return buf.toString('utf8', 0, n).trim().toLowerCase();
}

// ── Ollama ───────────────────────────────────────────────────────────────────
function ollamaRunning() {
  try {
    const code = execSync(`curl -s -o NUL -w "%{http_code}" ${OLLAMA_URL}/api/tags`,
      { encoding: 'utf8', timeout: 5000 }).trim();
    return code === '200';
  } catch { return false; }
}

function ollamaHasModel() {
  try {
    const out = execSync(`curl -s ${OLLAMA_URL}/api/tags`, { encoding: 'utf8', timeout: 5000 });
    return JSON.parse(out).models.some((m) => m.name === OLLAMA_MODEL || m.name.startsWith(OLLAMA_MODEL + ':'));
  } catch { return false; }
}

function pullModel() {
  process.stdout.write(`Pulling ${OLLAMA_MODEL} (first time can take a while)... `);
  try { execSync(`ollama pull ${OLLAMA_MODEL}`, { stdio: 'inherit' }); console.log('pulled ✓'); return true; }
  catch { console.log(`failed — pull it manually: ollama pull ${OLLAMA_MODEL}`); return false; }
}

// Send one "hi" through the OpenAI-compatible endpoint and confirm we get text back.
function pingModel() {
  process.stdout.write(`Saying "hi" to ${OLLAMA_MODEL}... `);
  try {
    const body = JSON.stringify({
      model: OLLAMA_MODEL,
      messages: [{ role: 'user', content: 'hi' }],
      stream: false,
    }).replace(/"/g, '\\"');
    const out = execSync(
      `curl -s -X POST ${OLLAMA_URL}/v1/chat/completions -H "Content-Type: application/json" -d "${body}"`,
      { encoding: 'utf8', timeout: 60000 });
    const reply = JSON.parse(out).choices?.[0]?.message?.content?.trim();
    if (reply) { console.log(`ok ✓\n  ${OLLAMA_MODEL}: ${reply.slice(0, 120)}`); return true; }
    console.log('no reply — model responded but content was empty.');
  } catch (e) { console.log(`failed — ${String(e.message || e).split('\n')[0]}`); }
  return false;
}

function ensureOllama() {
  console.log('\nChecking Ollama...');
  if (!ollamaRunning()) {
    console.log(`Ollama is NOT running at ${OLLAMA_URL}. Start it first: ollama serve`);
    while (true) {
      const ans = waitForEnter('When Ollama is up, press Enter to re-check (or type "skip" to continue): ');
      if (ans === 'skip') { console.log('Skipped Ollama.'); return false; }
      if (ollamaRunning()) break;
      console.log('Still not reachable. Give it a moment, then try again.');
    }
  }
  console.log('Ollama is running ✓');

  if (!ollamaHasModel()) {
    console.log(`Model ${OLLAMA_MODEL} not found locally.`);
    if (!pullModel() || !ollamaHasModel()) return false;
  } else {
    console.log(`Model ${OLLAMA_MODEL}: present ✓`);
  }

  return pingModel();
}

// ── speaches (STT/TTS, runs inside Docker) ───────────────────────────────────
function speachesRunning() {
  try {
    const out = execSync(`docker ps --filter "name=${SPEACHES_CONTAINER}" --format "{{.Names}}"`,
      { encoding: 'utf8' });
    return out.split('\n').map((s) => s.trim()).some((n) => n === SPEACHES_CONTAINER);
  } catch { return false; }
}

function ensureSpeaches() {
  console.log('\nChecking speaches server (inside Docker)...');
  if (speachesRunning()) { console.log('speaches: running ✓'); return; }
  if (!fs.existsSync(SPEACHES_COMPOSE)) {
    console.log(`speaches: not running and no compose file at ${SPEACHES_COMPOSE} — run setup-speaches.js.`);
    return;
  }
  process.stdout.write('speaches: starting via docker compose... ');
  try {
    execSync(`docker compose -f "${SPEACHES_COMPOSE}" up -d`, { stdio: 'ignore' });
    console.log('started ✓');
  } catch {
    console.log('failed — start it manually: node setup-speaches.js');
  }
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
    console.assert(OLLAMA_MODEL === 'gemma3:4b', 'ollama model');
    console.log('self-test OK');
    return;
  }

  // 1) Local LLM: Ollama up + gemma3:4b present + a real "hi" round-trip.
  ensureOllama();

  // 2) Docker Desktop (waits, re-checks on Enter); then speaches + the rest.
  if (ensureDocker()) {
    ensureSpeaches();
    ensureContainers();
  }

  checkManagerApi();
  if (!args.includes('--no-build')) build();
  launchWorker();
}

main();
