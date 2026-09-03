#!/bin/bash
# Loads .env and serves on localhost; put a TLS reverse proxy in front.
# getUserMedia refuses to run outside a secure context, so plain http
# works only on localhost.
cd "$(dirname "$0")"
set -a; [ -f .env ] && . ./.env; set +a
exec uvicorn app:app --host 127.0.0.1 --port "${PORT:-8100}"
