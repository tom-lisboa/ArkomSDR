#!/usr/bin/env python3
"""Publish only the built Svelte documentation, without restarting SDR workers."""
import argparse
import hashlib
import json
from pathlib import Path
import shlex
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def run(command, **kwargs):
    return subprocess.run(command, check=True, text=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="dev@24.199.100.186")
    parser.add_argument("--identity", default="~/.ssh/arkom_dev")
    args = parser.parse_args()
    dist = ROOT / "docs-portal/dist"
    contract = json.loads((ROOT / "internal/management/openapi.json").read_text())
    if json.loads((dist / "openapi.json").read_text()) != contract:
        raise SystemExit("Build is outdated. Run npm run check && npm run build in docs-portal.")
    files = sorted(p for p in dist.rglob("*") if p.is_file())
    allowed = {"index.html", "openapi.json", "guia-integracao.md", "favicon.svg"}
    digest = hashlib.sha256()
    for path in files:
        relative = path.relative_to(dist).as_posix()
        if path.is_symlink() or (relative not in allowed and not relative.startswith("assets/")):
            raise SystemExit(f"Unexpected public file: {relative}")
        digest.update(relative.encode() + b"\0" + path.read_bytes())
    release = "d-" + digest.hexdigest()[:12]
    identity = str(Path(args.identity).expanduser())
    ssh = ["ssh", "-i", identity, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", args.host]
    stage = run(ssh + ["mktemp -d /tmp/arkom-docs.XXXXXXXX"], capture_output=True).stdout.strip()
    if not stage.startswith("/tmp/arkom-docs.") or len(stage.splitlines()) != 1:
        raise SystemExit("Unexpected staging directory")
    with tempfile.TemporaryDirectory(prefix="arkom-docs-") as temporary:
        archive = Path(temporary) / "portal.tar.gz"
        with tarfile.open(archive, "w:gz") as tar:
            for path in files:
                tar.add(path, arcname=path.relative_to(dist), recursive=False)
        run(["scp", "-i", identity, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
             str(archive), str(ROOT / "deploy/Caddyfile"), f"{args.host}:{stage}/"])
    remote = r'''
import os, pathlib, shutil, subprocess, sys, tarfile
stage, release = pathlib.Path(sys.argv[1]), sys.argv[2]
base = pathlib.Path('/var/www/arkom-docs')
destination = base / 'releases' / release
current = base / 'current'
caddy = pathlib.Path('/etc/caddy/Caddyfile')
candidate = (stage / 'Caddyfile').read_text()
block = """    redir /docs /docs/ 308
    handle_path /docs/* {
        root * /var/www/arkom-docs/current
        header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
        file_server
    }
"""
previous_config = caddy.read_text()
if previous_config not in (candidate, candidate.replace(block, '')):
    raise SystemExit('Caddy changed independently; review the diff before publishing.')
if current.exists() and not current.is_symlink():
    raise SystemExit('The documentation current path must be a symlink.')
subprocess.run(['caddy', 'validate', '--config', str(stage / 'Caddyfile'), '--adapter', 'caddyfile'], check=True)
destination.mkdir(parents=True, exist_ok=True, mode=0o755)
with tarfile.open(stage / 'portal.tar.gz') as tar:
    for member in tar.getmembers():
        path = pathlib.PurePosixPath(member.name)
        if not member.isfile() or path.is_absolute() or '..' in path.parts:
            raise SystemExit('Invalid documentation archive')
    tar.extractall(destination, filter='data')
for directory, _, filenames in os.walk(base):
    os.chmod(directory, 0o755)
    for filename in filenames:
        path = pathlib.Path(directory) / filename
        if not path.is_symlink():
            os.chmod(path, 0o644)
old_target = os.readlink(current) if current.is_symlink() else None
backup = stage / 'Caddyfile.before'
backup.write_text(previous_config)
pending = base / ('current-' + release)
pending.symlink_to(destination)
try:
    pending.replace(current)
    shutil.copyfile(stage / 'Caddyfile', caddy)
    subprocess.run(['systemctl', 'reload', 'caddy'], check=True)
    subprocess.run(['curl', '--fail', '--silent', '--show-error', '--max-time', '30',
                    '--output', '/dev/null', 'https://24.199.100.186/docs/'], check=True)
except BaseException:
    shutil.copyfile(backup, caddy)
    if old_target:
        pending.symlink_to(old_target)
        pending.replace(current)
    elif current.is_symlink():
        current.unlink()
    subprocess.run(['systemctl', 'reload', 'caddy'], check=False)
    raise
print('Documentation release:', release)
print('URL: https://24.199.100.186/docs/')
print('Previous configuration preserved at:', backup)
'''
    run(ssh + ["sudo python3 - " + shlex.quote(stage) + " " + shlex.quote(release)], input=remote)


if __name__ == "__main__":
    main()
