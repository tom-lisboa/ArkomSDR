#!/usr/bin/env python3
"""Create/submit the eight specific cadence templates. Never sends a message."""
from pathlib import Path
import json
import subprocess

ROOT = Path(__file__).resolve().parents[1]
payloads = {p.stem: json.loads(p.read_text()) for p in (ROOT / 'deploy/cadence').glob('*.json')}
remote = 'payloads=' + repr(payloads) + '\n' + r'''
from pathlib import Path
import base64,json,urllib.request,urllib.error
values=dict(line.split('=',1) for line in Path('/etc/arkom-bia/runtime.env').read_text().splitlines() if '=' in line and not line.startswith('#'))
authorization=base64.b64encode((values['TWILIO_ACCOUNT_SID']+':'+values['TWILIO_AUTH_TOKEN']).encode()).decode()
def request(path,body=None):
    req=urllib.request.Request('https://content.twilio.com/v1/'+path,data=json.dumps(body).encode() if body is not None else None,
        headers={'Authorization':'Basic '+authorization,'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req,timeout=25) as response:return json.load(response)
    except urllib.error.HTTPError as error:
        response=json.load(error)
        raise SystemExit(json.dumps({'http_status':error.code,'code':response.get('code'),'message':response.get('message')}))
contents=[]
path='Content?PageSize=100'
while path:
    data=request(path);contents+=data.get('contents',[])
    next_url=data.get('meta',{}).get('next_page_url')
    path=next_url.split('/v1/',1)[1] if next_url else None
result={}
for name,payload in sorted(payloads.items()):
    matches=[c for c in contents if c['friendly_name']==payload['friendly_name']]
    assert len(matches)<=1,'Duplicate template name'
    if matches:
        content=matches[0]
        assert content['types']==payload['types'] and content['variables']==payload['variables'],'Existing content differs'
    else:content=request('Content',payload)
    sid=content['sid']
    approval=request('Content/'+sid+'/ApprovalRequests')['whatsapp']
    category='MARKETING' if name.startswith('no_reply_') else 'UTILITY'
    if approval['status']=='unsubmitted':
        approval=request('Content/'+sid+'/ApprovalRequests/whatsapp',{'name':payload['friendly_name'],'category':category})
    result[name]={'sid':sid,'status':approval['status'],'category':approval.get('category',category),'reason':approval.get('rejection_reason')}
    print(name,approval['status'],flush=True)
print('RESULT='+json.dumps(result))
'''
response=subprocess.run(['ssh','-i',str(Path.home()/'.ssh/arkom_dev'),'-o','IdentitiesOnly=yes','-o','BatchMode=yes',
    'dev@24.199.100.186','sudo python3 -'],input=remote,text=True,capture_output=True,timeout=300)
if response.returncode:
    print(response.stdout)
    raise SystemExit('Could not provision cadence templates; no runtime settings changed.')
lines=response.stdout.split('RESULT=',1)
print(lines[0],end='')
result=json.loads(lines[1])
(ROOT/'.local/deploy/cadence-templates-result.json').write_text(json.dumps(result,indent=2)+'\n')
print('Template metadata saved. No customer messages were sent.')
