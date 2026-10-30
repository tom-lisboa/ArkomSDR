#!/usr/bin/env python3
"""Publish the tested Go release and enable the approved direct-response cadences."""
from pathlib import Path
import hashlib, io, json, shlex, subprocess, tarfile

root=Path(__file__).resolve().parents[1]
metadata=json.loads((root/'.local/deploy/cadence-templates-result.json').read_text())
assert len(metadata)==8 and all(v['status']=='approved' if k.startswith('no_reply_') else v['status'] in ('approved','pending','received') for k,v in metadata.items()),'No-reply templates must be approved; meeting models must be approved or under review'
assets={k:json.loads((root/'deploy/cadence'/(k+'.json')).read_text()) for k in metadata}
binary=root/'.local/deploy/bia'
release='r-'+hashlib.sha256(binary.read_bytes()).hexdigest()[:12]
ssh=['ssh','-i',str(Path.home()/'.ssh/arkom_dev'),'-o','IdentitiesOnly=yes','-o','BatchMode=yes','dev@24.199.100.186']
def run(command,**kwargs):return subprocess.run(command,check=True,**kwargs)
verify='templates='+repr(metadata)+'\nassets='+repr(assets)+'\n'+r'''
from pathlib import Path
import urllib.request,base64,json
values=dict(line.split('=',1) for line in Path('/etc/arkom-bia/runtime.env').read_text().splitlines() if '=' in line and not line.startswith('#'))
authorization=base64.b64encode((values['TWILIO_ACCOUNT_SID']+':'+values['TWILIO_AUTH_TOKEN']).encode()).decode()
for name,meta in templates.items():
    def get(suffix):
        req=urllib.request.Request('https://content.twilio.com/v1/Content/'+meta['sid']+suffix,headers={'Authorization':'Basic '+authorization})
        with urllib.request.urlopen(req,timeout=20) as response:return json.load(response)
    content=get('');asset=assets[name]
    assert content['types']==asset['types'] and content['variables']==asset['variables'],'Template content mismatch'
    status=get('/ApprovalRequests')['whatsapp']['status']
    assert status=='approved' if name.startswith('no_reply_') else status in ('approved','pending','received'),'Template is rejected or unavailable'
    print(name,status,flush=True)
print('Cadence templates verified; pending meeting models remain guarded at each send.')
'''
run(ssh+['sudo python3 -'],input=verify,text=True,timeout=240)
sql="SELECT (SELECT count(*) FROM bia_go.voice_requests WHERE state IN ('requesting','dialing','queued','initiated','ringing','in-progress'))+(SELECT count(*) FROM bia_go.jobs j JOIN bia_go.leads l ON l.id=j.lead_id WHERE j.status='running' AND l.lease_until>now())+(SELECT count(*) FROM bia_go.worker_tasks WHERE status='running' AND lease_until>now());"
active=run(ssh+['cd /opt/arkom-bia/current && sudo docker compose exec -T postgres psql -U bia -d bia -Atc '+shlex.quote(sql)],capture_output=True,text=True,timeout=30).stdout.strip()
if active!='0':raise SystemExit('Active work; retry after the current operation finishes.')
previous=run(ssh+['readlink -f /opt/arkom-bia/current'],capture_output=True,text=True,timeout=30).stdout.strip()
assert previous.startswith('/opt/arkom-bia/releases/r-')
bundle=root/'.local/deploy'/(release+'.tar.gz')
with tarfile.open(bundle,'w:gz') as tar:
    tar.add(binary,arcname='bia')
    for path in (root/'deploy').iterdir():
        if path.is_dir() or path.name in ['compose.yaml','Dockerfile.runtime','activate.sh','backup.sh','arkom-bia-backup.service','arkom-bia-backup.timer','Caddyfile','install-reminder-audio.py','install-intro-video.py','enable-intro-video.py'] or path.suffix=='.json':tar.add(path,arcname=path.name)
    raw=('BIA_RELEASE='+release+'\n').encode();info=tarfile.TarInfo('.env');info.size=len(raw);info.mode=0o600;tar.addfile(info,io.BytesIO(raw))
run(['scp','-i',str(Path.home()/'.ssh/arkom_dev'),'-o','IdentitiesOnly=yes',str(bundle),'dev@24.199.100.186:/tmp/'+bundle.name],timeout=600)
updates={'BIA_NO_REPLY_ENABLED':'true','BIA_MEETING_CHECKINS_ENABLED':'true','BIA_DISABLE_FOLLOWUPS':'true'}
updates.update({'TWILIO_'+name.upper()+'_SID':v['sid'] for name,v in metadata.items()})
configure='updates='+repr(updates)+'\n'+r'''
from pathlib import Path
import datetime,os,tempfile
path=Path('/etc/arkom-bia/runtime.env');old=path.read_text()
existing=dict(line.split('=',1) for line in old.splitlines() if '=' in line and not line.startswith('#'))
updates['BIA_NO_REPLY_SINCE']=existing.get('BIA_NO_REPLY_SINCE') or datetime.datetime.now(datetime.timezone.utc).isoformat()
text='\n'.join(line for line in old.splitlines() if line.split('=',1)[0] not in updates)
text+='\n'+'\n'.join(k+'='+v for k,v in updates.items())+'\n'
with tempfile.NamedTemporaryFile(mode='w',dir=path.parent,delete=False) as stream:
    stream.write(text);stream.flush();os.fsync(stream.fileno());temporary=Path(stream.name)
temporary.chmod(0o600);os.replace(temporary,path)
print('New cadence activation timestamp:',updates['BIA_NO_REPLY_SINCE'])
'''
remote='/opt/arkom-bia/releases/'+release
activate=f'''set -eu
sudo install -d -m 0755 {shlex.quote(remote)}
sudo tar -xzf /tmp/{bundle.name} -C {shlex.quote(remote)}
sudo cp -p /etc/arkom-bia/runtime.env /etc/arkom-bia/runtime.env.before-{release}
sudo python3 - <<'PYREMOTE'
{configure}
PYREMOTE
cd {shlex.quote(remote)}
sudo sh activate.sh
'''
try:
    run(ssh+['sh -s'],input=activate,text=True,timeout=360)
except Exception:
    rollback=f'''set -eu
sudo cp -p /etc/arkom-bia/runtime.env.before-{release} /etc/arkom-bia/runtime.env
cd {shlex.quote(remote)}
sudo docker compose stop api context execution media scheduling followup voice
sudo docker compose exec -T postgres psql -U bia -d bia -c "UPDATE bia_go.jobs SET status='skipped',error_code='proactive_rollout_rolled_back',completed_at=now() WHERE status IN ('pending','running') AND (payload->>'purpose' LIKE 'no_reply_%' OR payload->>'purpose' LIKE 'meeting_checkin_%');"
cd {shlex.quote(previous)}
sudo sh activate.sh
'''
    run(ssh+['sh -s'],input=rollback,text=True,timeout=360)
    raise
(root/'.local/deploy/last-proactive-release').write_text(release+'\n')
print('Published and enabled',release)
