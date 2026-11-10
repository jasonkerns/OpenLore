#!/bin/sh
# Container entrypoint. The first start on an empty /data writes a locked-down config (no anonymous access, SSH off)
# and prints a one-hour setup code to the log; paste it into Agent Harness → Settings → Knowledge base. Later starts
# keep the existing config. For a new setup code:
#   openlore harness setup-code --config /data/openlore.yml   (e.g. via ECS Exec)
set -eu
# With arguments, run that openlore command instead of the server: a one-off task with a command override, e.g.
#   harness restore --dir /data --from /data/backups/<file>.tar.gz   (with the service at desired tasks 0)
if [ "$#" -gt 0 ]; then
  exec openlore "$@"
fi
: "${OPENLORE_URL:?set OPENLORE_URL to the public https:// address, e.g. https://lore.example.com}"
openlore harness init --dir /data --url "$OPENLORE_URL" --owner "${OPENLORE_OWNER:-owner}" --http-port "${PORT:-8080}"
# Run from the persistent volume so the SSH host key (host_key_path defaults to the
# relative .ssh/openlore_ed25519) is created on /data, not the read-only root as uid 1000.
cd /data
exec openlore -c /data/openlore.yml /data/docs
