#!/bin/sh
set -eu
umask 077
mkdir -p /var/backups/arkom-bia
exec 9>/var/backups/arkom-bia/.lock
flock -n 9 || exit 0
cd /opt/arkom-bia/current
stamp=$(date -u +%Y%m%dT%H%M%SZ)
target=/var/backups/arkom-bia/bia-$stamp.dump
trap 'rm -f "$target.partial"' EXIT
docker compose exec -T postgres pg_dump -U bia -d bia -Fc > "$target.partial"
test -s "$target.partial"
docker compose exec -T postgres pg_restore --list < "$target.partial" >/dev/null
mv "$target.partial" "$target"
find /var/backups/arkom-bia -maxdepth 1 -name 'bia-*.dump' -type f -mtime +7 -delete
