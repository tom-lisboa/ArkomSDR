#!/bin/sh
# Run as root from the uploaded release directory, after provisioning /etc/arkom-bia.
set -eu
test "$(id -u)" = 0
release_dir=$(pwd -P)
case "$release_dir" in /opt/arkom-bia/releases/r-*) ;; *) echo 'Invalid release directory' >&2; exit 1 ;; esac
release=$(basename "$release_dir")
install -d -m 0755 /var/lib/arkom-bia/media/reminders
docker compose config --quiet
chmod 0555 bia
docker build -f Dockerfile.runtime -t "arkom-bia:$release" .
docker compose pull --quiet postgres redis
if test -x /opt/arkom-bia/current/backup.sh; then
    /opt/arkom-bia/current/backup.sh
fi
docker compose up -d --wait postgres redis
docker compose run --rm migrate
docker compose up -d api context execution media scheduling followup voice
curl --retry 15 --retry-all-errors --retry-delay 2 --max-time 5 --fail --silent http://127.0.0.1:8080/readyz
curl --retry 15 --retry-all-errors --retry-delay 2 --max-time 5 --fail --silent http://127.0.0.1:8081/healthz
chmod 0755 backup.sh
ln -sfn "$release_dir" /opt/arkom-bia/current
install -m 0644 arkom-bia-backup.service arkom-bia-backup.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now arkom-bia-backup.timer
systemctl start arkom-bia-backup.service
docker compose ps
