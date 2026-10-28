#!/usr/bin/env python3
"""Run as root on the server: install a pre-converted OGG/Opus recording, disabled."""
import argparse
import hashlib
import os
from pathlib import Path
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("audio", type=Path)
args = parser.parse_args()
if os.geteuid() != 0:
    parser.error("run with sudo on the deployment server")
if not args.audio.is_file() or not 32 <= args.audio.stat().st_size <= 16 * 1024 * 1024:
    parser.error("invalid audio size or path")
data = args.audio.read_bytes()
if not data.startswith(b"OggS") or b"OpusHead" not in data[:4096]:
    parser.error("expected OGG/Opus; run scripts/prepare-reminder-audio.py first")
digest = hashlib.sha256(data).hexdigest()
directory = Path("/var/lib/arkom-bia/media/reminders")
directory.mkdir(parents=True, exist_ok=True)
directory.chmod(0o755)
target = directory / (digest + ".ogg")
if target.exists():
    if target.read_bytes() != data:
        parser.error("immutable target checksum mismatch")
else:
    with tempfile.NamedTemporaryFile(dir=directory, delete=False) as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
        staged = Path(f.name)
    staged.chmod(0o444)
    os.replace(staged, target)
env = Path("/etc/arkom-bia/runtime.env")
updates = {"BIA_REMINDER_AUDIO_DIR": "/media/reminders", "BIA_REMINDER_AUDIO_SHA256": digest,
           "BIA_MEETING_REMINDERS_ENABLED": "false"}
lines = [line for line in env.read_text().splitlines() if line.split("=", 1)[0] not in updates]
lines.extend(f"{k}={v}" for k, v in updates.items())
with tempfile.NamedTemporaryFile(mode="w", dir=env.parent, delete=False) as f:
    f.write("\n".join(lines) + "\n")
    f.flush()
    os.fsync(f.fileno())
    staged = Path(f.name)
staged.chmod(0o600)
os.replace(staged, env)
print(f"Áudio instalado: {target}")
print("Cadência configurada como desativada. Recrie os serviços para aplicar a configuração.")
