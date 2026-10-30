#!/usr/bin/env python3
"""Validate and install Lumi configuration from the local .env without printing keys."""
import argparse
import json
from pathlib import Path
import shlex
import subprocess
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', default='dev@24.199.100.186')
    parser.add_argument('--identity', default='~/.ssh/arkom_dev')
    args = parser.parse_args()
    values = {}
    for line in (ROOT / '.env').read_text().splitlines():
        if '=' in line and not line.lstrip().startswith('#'):
            key, value = line.split('=', 1)
            values[key.strip()] = value.strip().strip('\"\'')
    config = {key: values.get(key, default) for key, default in {
        'BIA_LUMI_URL': 'https://lume-io.com',
        'BIA_LUMI_USER_API_KEY': '',
        'BIA_SALES_STALLED_AFTER': '30m',
    }.items()}
    if not config['BIA_LUMI_USER_API_KEY']:
        raise SystemExit('Preencha BIA_LUMI_USER_API_KEY no .env local antes de configurar a Lumi.')
    url = urlparse(config['BIA_LUMI_URL'])
    if url.scheme != 'https' or not url.hostname or url.username or url.password or url.path not in ('', '/') or url.query or url.fragment:
        raise SystemExit('BIA_LUMI_URL deve ser uma origem HTTPS válida.')
    if any('\n' in value or '\r' in value for value in config.values()):
        raise SystemExit('Configuração inválida.')
    remote = r'''
import datetime, json, os, pathlib, subprocess, sys, tempfile, urllib.request
settings = json.load(sys.stdin)
request = urllib.request.Request(settings['BIA_LUMI_URL'].rstrip('/') + '/api/instances',
    headers={'X-API-Key': settings['BIA_LUMI_USER_API_KEY']})
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None
try:
    with urllib.request.build_opener(NoRedirect()).open(request, timeout=15) as response:
        assert response.status == 200
        result = json.loads(response.read(1048576))
        assert isinstance(result, list)
except Exception:
    raise SystemExit('A Lumi recusou a consulta de instancias; configuracao anterior preservada.')
runtime = pathlib.Path('/etc/arkom-bia/runtime.env')
old = runtime.read_text()
new = '\n'.join(line for line in old.splitlines() if line.split('=', 1)[0] not in settings)
new += '\n' + '\n'.join(key + '=' + value for key, value in settings.items()) + '\n'
backup = runtime.with_name('runtime.env.before-lumi-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d%H%M%S'))
backup.write_text(old)
backup.chmod(0o600)
with tempfile.NamedTemporaryFile(mode='w', dir=runtime.parent, delete=False) as stream:
    stream.write(new)
    stream.flush()
    os.fsync(stream.fileno())
    temporary = pathlib.Path(stream.name)
temporary.chmod(0o600)
os.replace(temporary, runtime)
command = ['docker','compose','up','-d','--no-deps','--force-recreate','api','followup']
try:
    subprocess.run(command, cwd='/opt/arkom-bia/current', check=True, timeout=120, capture_output=True)
    subprocess.run(['curl','--retry','12','--retry-all-errors','--retry-delay','2','--fail','--silent','--max-time','5',
                    'http://127.0.0.1:8080/readyz'], check=True, timeout=90, capture_output=True)
except Exception:
    runtime.write_text(old)
    runtime.chmod(0o600)
    subprocess.run(command, cwd='/opt/arkom-bia/current', capture_output=True, timeout=120)
    raise SystemExit('Falha ao ativar a Lumi; configuracao anterior restaurada.')
print('Lumi configurada. API e followup ativos; nenhuma instancia ou mensagem de teste foi criada.')
'''
    subprocess.run(['ssh', '-i', str(Path(args.identity).expanduser()), '-o', 'IdentitiesOnly=yes',
                    '-o', 'BatchMode=yes', args.host, 'sudo python3 -c ' + shlex.quote(remote)],
                   input=json.dumps(config), text=True, check=True)


if __name__ == '__main__':
    main()
