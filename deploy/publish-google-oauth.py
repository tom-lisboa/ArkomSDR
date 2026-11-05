#!/usr/bin/env python3
"""Publish Google OAuth without changing the SDR cadence or provider URLs.

--configure-only reads the two Google credentials from the private local .env,
activates them on the existing OAuth-capable release and recreates only the API.
Secrets travel through SSH stdin, never through command arguments or output.
"""
from pathlib import Path
import argparse
import hashlib
import io
import json
import re
import shlex
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]
SSH = ["ssh", "-i", str(Path.home() / ".ssh/arkom_dev"), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "dev@24.199.100.186"]


def credentials():
    result = {}
    for line in (ROOT / ".env").read_text().splitlines():
        if "=" not in line or line.lstrip().startswith("#"):
            continue
        key, value = line.split("=", 1)
        key, value = key.strip(), value.strip()
        if key not in ("BIA_GOOGLE_CLIENT_ID", "BIA_GOOGLE_CLIENT_SECRET"):
            continue
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        if key in result:
            raise SystemExit("Duplicate Google credential field in .env; resolve before publishing.")
        result[key] = value
    if not re.fullmatch(r"[A-Za-z0-9.-]{10,512}\.apps\.googleusercontent\.com", result.get("BIA_GOOGLE_CLIENT_ID", "")) or not re.fullmatch(r"[A-Za-z0-9._-]{10,1024}", result.get("BIA_GOOGLE_CLIENT_SECRET", "")):
        raise SystemExit("Fill both Google OAuth credentials in the private .env first.")
    return result


REMOTE = r'''
from pathlib import Path
import fcntl, hashlib, json, os, secrets, shutil, subprocess, sys, tarfile, tempfile, urllib.request, urllib.error
request = json.load(sys.stdin)
release, configure_only = request['release'], request['configure_only']
services = ['api','context','execution','media','scheduling','followup','voice']
lock = open('/var/lock/arkom-bia-deploy.lock','w')
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
current = Path('/opt/arkom-bia/current')
previous = current.resolve()
if not str(previous).startswith('/opt/arkom-bia/releases/r-'):
    raise SystemExit('Unexpected active release path.')
def compose(directory, *args, **kwargs):
    return subprocess.run(['docker','compose',*args],cwd=directory,check=True,**kwargs)
sql = "SELECT (SELECT count(*) FROM bia_go.voice_requests WHERE state IN ('requesting','dialing','queued','initiated','ringing','in-progress'))+(SELECT count(*) FROM bia_go.jobs j JOIN bia_go.leads l ON l.id=j.lead_id WHERE j.status='running' AND l.lease_until>now())+(SELECT count(*) FROM bia_go.worker_tasks WHERE status='running' AND lease_until>now())+(SELECT count(*) FROM bia_go.google_oauth_states WHERE started_at IS NOT NULL AND expires_at>now() AND (consumed_at IS NULL OR consumed_at>now()-interval '30 seconds'));"
active = compose(previous,'exec','-T','postgres','psql','-U','bia','-d','bia','-Atc',sql,capture_output=True,text=True).stdout.strip()
if active != '0':
    raise SystemExit('Active SDR work; retry after that operation finishes.')
if configure_only:
    try:
        r=urllib.request.urlopen('http://127.0.0.1:8080/v1/management/integrations/google/callback',timeout=10)
    except urllib.error.HTTPError as e:
        r=e
    with r:
        payload=json.load(r)
    if payload.get('error') not in ('google_not_configured','google_invalid_state'):
        raise SystemExit('Current release does not expose the Google OAuth callback.')
    destination = previous
else:
    destination = Path('/opt/arkom-bia/releases') / release
    destination.mkdir(mode=0o755,exist_ok=True)
    with tarfile.open('/tmp/'+release+'-google.tar.gz') as archive:
        for member in archive.getmembers():
            path=Path(member.name)
            if path.is_absolute() or '..' in path.parts or not (member.isfile() or member.isdir()):
                raise SystemExit('Unsafe release archive.')
        archive.extractall(destination,filter='data')
    actual=hashlib.sha256((destination/'bia').read_bytes()).hexdigest()
    if actual!=request['binary_sha256']:
        raise SystemExit('Uploaded binary digest mismatch.')
runtime=Path('/etc/arkom-bia/runtime.env')
old=runtime.read_text()
values=dict(line.split('=',1) for line in old.splitlines() if '=' in line and not line.startswith('#'))
backup_base=runtime.parent/'backups';backup_base.mkdir(mode=0o700,exist_ok=True)
backup=Path(tempfile.mkdtemp(prefix='google-',dir=backup_base))
shutil.copy2(runtime,backup/'runtime.env')
redirect='https://sdk.arkom.ai/v1/management/integrations/google/callback'
if values.get('BIA_GOOGLE_REDIRECT_URL') not in (None,'',redirect):
    raise SystemExit('Existing Google redirect differs; review before publishing.')
updates={'BIA_GOOGLE_REDIRECT_URL':redirect}
for key in ['BIA_GOOGLE_CLIENT_ID','BIA_GOOGLE_CLIENT_SECRET']:
    if key not in values:
        updates[key]=''
if not values.get('BIA_GOOGLE_TOKEN_KEY'):
    updates['BIA_GOOGLE_TOKEN_KEY']=secrets.token_hex(32)
elif len(values['BIA_GOOGLE_TOKEN_KEY'])<32:
    raise SystemExit('Existing Google encryption key is invalid; review before publishing.')
if configure_only:
    updates.update(request['credentials'])
    updates['BIA_GOOGLE_ENABLED']='true'
else:
    updates['BIA_GOOGLE_ENABLED']=values.get('BIA_GOOGLE_ENABLED') or 'false'
if request.get('enable_scheduling'):
    if updates['BIA_GOOGLE_ENABLED']!='true':
        raise SystemExit('Google OAuth must be enabled before scheduling.')
    updates['BIA_GOOGLE_SCHEDULING_ENABLED']='true'
origins=[o.strip() for o in values.get('BIA_MANAGEMENT_ORIGINS','').split(',') if o.strip()]
for origin in ['https://sdk.arkom.ai','https://arkom-painelsdr.vercel.app']:
    if origin not in origins:origins.append(origin)
updates['BIA_MANAGEMENT_ORIGINS']=','.join(origins)
text='\n'.join(line for line in old.splitlines() if line.split('=',1)[0] not in updates)+'\n'
text+='\n'.join(k+'='+v for k,v in updates.items())+'\n'
with tempfile.NamedTemporaryFile(mode='w',dir=runtime.parent,delete=False) as stream:
    stream.write(text);stream.flush();os.fsync(stream.fileno());pending=Path(stream.name)
pending.chmod(0o600);os.replace(pending,runtime)
try:
    if configure_only:
        compose(destination,'up','-d','--no-deps','api')
    else:
        subprocess.run(['sh','activate.sh'],cwd=destination,check=True)
    subprocess.run(['curl','--fail','--silent','--show-error','--retry','15','--retry-all-errors','--retry-delay','1','--max-time','5','http://127.0.0.1:8080/readyz'],check=True)
    version=compose(destination,'exec','-T','postgres','psql','-U','bia','-d','bia','-Atc','SELECT max(version) FROM bia_go.schema_versions;',capture_output=True,text=True).stdout.strip()
    if (not configure_only and version!='22') or (configure_only and int(version)<20):raise SystemExit('Unexpected database schema version after publishing.')
except BaseException:
    shutil.copy2(backup/'runtime.env',runtime)
    if configure_only:
        compose(previous,'up','-d','--no-deps','api')
    else:
        compose(destination,'stop',*services)
        compose(previous,'up','-d',*services)
        pending=current.with_name('current-google-rollback');pending.unlink(missing_ok=True)
        pending.symlink_to(previous);pending.replace(current)
    raise
print(json.dumps({'release':destination.name,'schema':int(version),'google_enabled':updates['BIA_GOOGLE_ENABLED']=='true','configure_only':configure_only,'runtime_backup':str(backup),'voice_urls_preserved':True}))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--configure-only", action="store_true")
    parser.add_argument("--enable-scheduling", action="store_true", help="Enable PostgreSQL and Google scheduling for the internal SDR")
    args = parser.parse_args()
    if args.configure_only and args.enable_scheduling:
        parser.error("Scheduling activation requires a full worker release")
    payload = {"configure_only": args.configure_only, "enable_scheduling": args.enable_scheduling, "release": "", "credentials": {}}
    if args.configure_only:
        payload["credentials"] = credentials()
    else:
        binary = ROOT / ".local/deploy/bia"
        payload["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        release = payload["release"] = "r-" + payload["binary_sha256"][:12]
        bundle = ROOT / ".local/deploy" / (release + "-google.tar.gz")
        with tarfile.open(bundle, "w:gz") as archive:
            archive.add(binary, arcname="bia")
            for name in ["compose.yaml", "Dockerfile.runtime", "activate.sh", "backup.sh", "arkom-bia-backup.service", "arkom-bia-backup.timer", "cadence", "quick-replies"]:
                archive.add(ROOT / "deploy" / name, arcname=name)
            raw = ("BIA_RELEASE=" + release + "\n").encode()
            info = tarfile.TarInfo(".env"); info.size = len(raw); info.mode = 0o600
            archive.addfile(info, io.BytesIO(raw))
        subprocess.run(["scp", "-i", str(Path.home() / ".ssh/arkom_dev"), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", str(bundle), "dev@24.199.100.186:/tmp/" + bundle.name], check=True, timeout=180)
    # Credentials are never part of the command line, stdout or a release file.
    subprocess.run(SSH + ["sudo python3 -c " + shlex.quote(REMOTE)], input=json.dumps(payload), text=True, check=True, timeout=360)


if __name__ == "__main__":
    main()
