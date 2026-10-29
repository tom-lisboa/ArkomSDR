#!/usr/bin/env python3
"""Enable the installed case only after its matching WhatsApp template is approved."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('content_sid')
args = parser.parse_args()
if os.geteuid() != 0:
    parser.error('run with sudo on the deployment host')
if not re.fullmatch(r'HX[0-9a-fA-F]{32}', args.content_sid):
    parser.error('expected a Content SID')
env = Path('/etc/arkom-bia/runtime.env')
values = dict(line.split('=', 1) for line in env.read_text().splitlines()
              if '=' in line and not line.startswith('#'))
digest = values.get('BIA_INTRO_VIDEO_SHA256', '')
if not re.fullmatch(r'[0-9a-f]{64}', digest):
    parser.error('install the video first')
video = Path('/var/lib/arkom-bia/media/cases') / (digest + '.mp4')
if hashlib.sha256(video.read_bytes()).hexdigest() != digest:
    parser.error('installed video checksum mismatch')
origin = values.get('BIA_VOICE_PUBLIC_URL', '').rstrip('/')
url = origin + '/media/cases/' + digest + '.mp4'
auth = base64.b64encode((values['TWILIO_ACCOUNT_SID'] + ':' + values['TWILIO_AUTH_TOKEN']).encode()).decode()
def fetch(path):
    req = urllib.request.Request('https://content.twilio.com/v1/Content/' + args.content_sid + path,
                                 headers={'Authorization': 'Basic ' + auth})
    with urllib.request.urlopen(req, timeout=25) as response:
        return json.load(response)
content = fetch('')
if content.get('types', {}).get('twilio/media', {}).get('media') != [url]:
    parser.error('template media does not match the installed video')
status = fetch('/ApprovalRequests')['whatsapp']['status']
if status != 'approved':
    raise SystemExit('Not enabled; WhatsApp template status: ' + status)
updates = {'BIA_INTRO_VIDEO_ENABLED': 'true', 'TWILIO_INTRO_VIDEO_CONTENT_SID': args.content_sid}
lines = [line for line in env.read_text().splitlines() if line.split('=', 1)[0] not in updates]
lines.extend(key + '=' + value for key, value in updates.items())
with tempfile.NamedTemporaryFile(mode='w', dir=env.parent, delete=False) as staged:
    staged.write('\n'.join(lines) + '\n'); staged.flush(); os.fsync(staged.fileno())
    temp = Path(staged.name)
temp.chmod(0o600)
os.replace(temp, env)
print('Intro video enabled in runtime.env; recreate API/workers to apply.')
