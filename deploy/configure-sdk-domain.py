#!/usr/bin/env python3
"""Add the SDK virtual host without replacing other apps on the shared server."""
from pathlib import Path
import argparse
import json
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="dev@24.199.100.186")
    parser.add_argument("--identity", default="~/.ssh/arkom_dev")
    args = parser.parse_args()
    ssh = ["ssh", "-i", str(Path(args.identity).expanduser()), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", args.host]
    block = (ROOT / "deploy/Caddyfile.sdk").read_text()
    remote = r'''
from pathlib import Path
import json, os, shutil, subprocess, tempfile
block = BLOCK
config = Path('/etc/caddy/Caddyfile')
managed = Path('/etc/caddy/arkom-sdk.caddy')
include = 'import /etc/caddy/arkom-sdk.caddy'
previous = config.read_text()
if 'sdk.arkom.ai' in previous:
    raise SystemExit('Domain is already configured directly; review before changing it.')
if previous.count(include) > 1:
    raise SystemExit('Duplicate SDK imports; review before changing them.')
backup_root = Path('/etc/caddy/backups')
backup_root.mkdir(mode=0o700, exist_ok=True)
backup = Path(tempfile.mkdtemp(prefix='sdk-', dir=backup_root))
shutil.copy2(config, backup / 'Caddyfile')
had_managed = managed.exists()
if had_managed:
    shutil.copy2(managed, backup / 'arkom-sdk.caddy')
candidate = config.with_name('.Caddyfile.sdk-candidate')
pending = managed.with_name('.arkom-sdk.caddy-pending')
pending.write_text(block)
os.chmod(pending, 0o644)
pending.replace(managed)
candidate.write_text(previous if include in previous.splitlines() else previous.rstrip() + '\n\n' + include + '\n')
os.chmod(candidate, 0o644)
try:
    subprocess.run(['caddy', 'validate', '--config', str(candidate), '--adapter', 'caddyfile'], check=True)
    candidate.replace(config)
    subprocess.run(['systemctl', 'reload', 'caddy'], check=True)
except BaseException:
    shutil.copy2(backup / 'Caddyfile', config)
    if had_managed:
        shutil.copy2(backup / 'arkom-sdk.caddy', managed)
    else:
        managed.unlink(missing_ok=True)
    subprocess.run(['systemctl', 'reload', 'caddy'], check=False)
    raise
finally:
    candidate.unlink(missing_ok=True)
print(json.dumps({'domain':'sdk.arkom.ai','backup':str(backup),'caddy_reloaded':True,'application_restarted':False}))
'''.replace('BLOCK', repr(block), 1)
    result = subprocess.run(ssh + ["sudo python3 -"], input=remote, text=True, check=True, capture_output=True)
    configuration = json.loads(result.stdout.strip().splitlines()[-1])
    print(json.dumps(configuration))
    evidence = ROOT / ".local/deploy/sdk-domain"
    evidence.mkdir(parents=True, exist_ok=True)
    (evidence / "configuration.json").write_text(json.dumps(configuration, indent=2) + "\n")


if __name__ == "__main__":
    main()
