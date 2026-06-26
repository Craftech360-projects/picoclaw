#!/usr/bin/env node
// Run speaches (OpenAI-compatible STT/TTS) locally and point picoclaw's
// OPENAI_BASE_URL at this machine's LAN IP.
//   node setup-speaches.js              # set IP in .env, start speaches, wait until ready
//   node setup-speaches.js 192.168.0.50 # force a specific IP
//   node setup-speaches.js --self-test

const fs = require('fs');
const os = require('os');
const path = require('path');
const { execSync } = require('child_process');

const ROOT = __dirname;
const ENV_FILE = path.join(ROOT, '.env');
const COMPOSE = path.join(ROOT, 'speaches', 'docker-compose.yml');
const PORT = 8000;

function detectIp() {
  const cands = [];
  for (const addrs of Object.values(os.networkInterfaces()))
    for (const a of addrs) if (a.family === 'IPv4' && !a.internal) cands.push(a.address);
  const priv = (ip) => /^192\.168\./.test(ip) || /^10\./.test(ip) || /^172\.(1[6-9]|2\d|3[01])\./.test(ip);
  return cands.find(priv) || cands[0];
}

// Replace the OPENAI_BASE_URL line's host so it points at `ip`. Returns true if changed.
function setBaseUrl(ip) {
  const url = `http://${ip}:${PORT}/v1`;
  const text = fs.readFileSync(ENV_FILE, 'utf8');
  const re = /^(\s*OPENAI_BASE_URL\s*=\s*).*$/m;
  if (!re.test(text)) throw new Error('OPENAI_BASE_URL not found in .env');
  const next = text.replace(re, `$1"${url}"`);
  if (next === text) { console.log(`OPENAI_BASE_URL already ${url}`); return false; }
  fs.writeFileSync(ENV_FILE, next);
  console.log(`OPENAI_BASE_URL -> ${url}`);
  return true;
}

function dockerRunning() {
  try { execSync('docker info', { stdio: 'ignore' }); return true; } catch { return false; }
}

function ready() {
  try {
    const code = execSync(`curl -s -o NUL -w "%{http_code}" http://localhost:${PORT}/v1/models`,
      { encoding: 'utf8', timeout: 5000 }).trim();
    return code === '200';
  } catch { return false; }
}

function sleepSec(s) { Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, s * 1000); }

function main() {
  const args = process.argv.slice(2);
  if (args.includes('--self-test')) {
    const re = /^(\s*OPENAI_BASE_URL\s*=\s*).*$/m;
    const sample = 'OPENAI_BASE_URL="http://192.168.0.68:8000/v1"';
    console.assert(sample.replace(re, '$1"http://1.2.3.4:8000/v1"') === 'OPENAI_BASE_URL="http://1.2.3.4:8000/v1"', 'rewrite');
    console.log('self-test OK');
    return;
  }

  const ip = args.find((a) => /^\d+\.\d+\.\d+\.\d+$/.test(a)) || detectIp();
  if (!ip) throw new Error('No LAN IPv4 detected. Pass one: node setup-speaches.js <ip>');
  console.log(`System IP: ${ip}`);
  setBaseUrl(ip);

  if (!dockerRunning()) {
    console.log('\nDocker is not running. Start Docker Desktop, then re-run.');
    return;
  }

  console.log('\nStarting speaches (first run pulls the image — can take a while)...');
  execSync(`docker compose -f "${COMPOSE}" up -d`, { stdio: 'inherit' });

  process.stdout.write('Waiting for speaches to be ready');
  for (let i = 0; i < 60; i++) {            // up to ~3 min
    if (ready()) { console.log(`\nspeaches is ready ✓  http://${ip}:${PORT}/v1`); return; }
    process.stdout.write('.'); sleepSec(3);
  }
  console.log('\nNot ready yet. Check logs: docker compose -f speaches/docker-compose.yml logs -f');
}

main();
