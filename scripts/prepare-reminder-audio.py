#!/usr/bin/env python3
"""Convert the supplied recording to OGG/Opus locally; never uploads or sends it."""
import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("audio", type=Path, help="Original recording")
args = parser.parse_args()
if not args.audio.is_file():
    parser.error("audio file not found")
if not shutil.which("ffmpeg"):
    parser.error("ffmpeg is required (macOS: brew install ffmpeg)")
directory = Path(__file__).resolve().parents[1] / ".local/media/reminders"
directory.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(dir=directory) as tmp:
    converted = Path(tmp) / "reminder.ogg"
    subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-i", str(args.audio.resolve()),
                    "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "48000", "-c:a", "libopus",
                    "-b:a", "32k", "-application", "voip", "-f", "ogg", str(converted)], check=True)
    data = converted.read_bytes()
    if not 32 <= len(data) <= 16 * 1024 * 1024:
        parser.error("converted recording must be between 32 bytes and 16 MiB")
    digest = hashlib.sha256(data).hexdigest()
    target = directory / (digest + ".ogg")
    if target.exists() and target.read_bytes() != data:
        parser.error("existing file failed checksum")
    if not target.exists():
        os.replace(converted, target)
        target.chmod(0o444)
print(f"Arquivo: {target}")
print(f"BIA_REMINDER_AUDIO_SHA256={digest}")
print("Preparado localmente. Nenhuma mensagem enviada e nenhuma cadência ativada.")
