#!/usr/bin/env bash
# Reset the public demo to fresh LotR synthetic data.
# Safe to run from cron. Does not touch config or keys.
set -euo pipefail

UNIT=aegis-demo
DATA_DIR=/var/lib/aegis-demo
BIN=/opt/aegis-demo/aegis

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

systemctl stop "$UNIT"

# Keep logs and config-on-disk out of the wipe: only mutable demo state.
rm -rf "$DATA_DIR"/mail-archive.db* \
       "$DATA_DIR"/copy_ledger.db* \
       "$DATA_DIR"/quicken.db* \
       "$DATA_DIR"/timeline.db* \
       "$DATA_DIR"/signal.db* \
       "$DATA_DIR"/audit_log.db* \
       "$DATA_DIR"/entity_links.json \
       "$DATA_DIR"/sample

install -d -o aegis-demo -g aegis-demo "$DATA_DIR"
sudo -u aegis-demo "$BIN" -data-dir "$DATA_DIR" -seed-lotr

systemctl start "$UNIT"
sleep 1
curl -fsS http://127.0.0.1:8090/healthz >/dev/null
echo "aegis-demo reset ok"
