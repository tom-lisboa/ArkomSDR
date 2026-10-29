#!/usr/bin/env python3
"""Install a verified MP4 on the deployment host without enabling outreach."""
import argparse
import hashlib
import os
from pathlib import Path
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('video', type=Path)
args = parser.parse_args()
if os.geteuid() != 0:
    parser.error('run with sudo on the deployment host')
if not args.video.is_file() or not 32 <= args.video.stat().st_size <= 16 * 1024 * 1024:
    parser.error('video must be a regular MP4 of at most 16 MiB')
data = args.video.read_bytes()
if data[4:8] != b'ftyp':
    parser.error('expected MP4; verify H.264 video and AAC audio before installing')
digest = hashlib.sha256(data).hexdigest()
directory = Path('/var/lib/arkom-bia/media/cases')
directory.mkdir(parents=True, exist_ok=True)
directory.chmod(0o755)
target = directory / (digest + '.mp4')
if target.exists():
    if target.read_bytes() != data:
        parser.error('immutable target checksum mismatch')
else:
    with tempfile.NamedTemporaryFile(dir=directory, delete=False) as staged:
        staged.write(data); staged.flush(); os.fsync(staged.fileno())
        temp = Path(staged.name)
    temp.chmod(0o444)
    os.replace(temp, target)
env = Path('/etc/arkom-bia/runtime.env')
updates = {'BIA_INTRO_VIDEO_DIR': '/media/cases', 'BIA_INTRO_VIDEO_SHA256': digest,
           'BIA_INTRO_VIDEO_ENABLED': 'false'}
lines = [line for line in env.read_text().splitlines() if line.split('=', 1)[0] not in updates]
lines.extend(key + '=' + value for key, value in updates.items())
with tempfile.NamedTemporaryFile(mode='w', dir=env.parent, delete=False) as staged:
    staged.write('\n'.join(lines) + '\n'); staged.flush(); os.fsync(staged.fileno())
    temp = Path(staged.name)
temp.chmod(0o600)
os.replace(temp, env)
print('MP4 installed:', digest, 'bytes:', len(data))
print('Intro video remains disabled until its template is approved and enabled.')
