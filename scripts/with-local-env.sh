#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ ! -f .env ]; then
  printf '%s\n' 'Run make env first.' >&2
  exit 1
fi
set -a
. ./.env
set +a
mkdir -p work/rocketmq
# The isolated local Proxy exposes plaintext gRPC; deployed endpoints need TLS.
exec env "rocketmq.client.logRoot=$PWD/work/rocketmq" "rocketmq.client.enableSsl=false" "$@"
