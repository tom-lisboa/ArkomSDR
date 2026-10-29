#!/usr/bin/env python3
"""Run synthetic SDR conversations. Only OpenAI is contacted; no real messages."""
from pathlib import Path
import os
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
env = os.environ.copy()
dotenv = root / '.env'
if dotenv.exists():
    for line in dotenv.read_text().splitlines():
        if '=' not in line or line.lstrip().startswith('#'):
            continue
        key, value = line.split('=', 1)
        if key.strip() in ('OPENAI_API_KEY', 'OPENAI_MODEL') and not env.get(key.strip()):
            env[key.strip()] = value.strip().strip('"').strip("'")
if not env.get('OPENAI_API_KEY') or not env.get('OPENAI_MODEL'):
    raise SystemExit('Configure OPENAI_API_KEY and OPENAI_MODEL in .env or environment')
raise SystemExit(subprocess.call(['go', 'run', './cmd/eval-conversations', *sys.argv[1:]], cwd=root, env=env))
