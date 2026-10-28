#!/usr/bin/env python3
"""Run on the server as root, only in shadow mode with OpenAI disabled."""
import datetime
import json
from pathlib import Path
import secrets
import time
import urllib.request
import urllib.error

config = dict(line.split("=", 1) for line in Path("/etc/arkom-bia/runtime.env").read_text().splitlines() if "=" in line and not line.startswith("#"))
assert config["BIA_MODE"] == "shadow"
assert config.get("BIA_VOICE_ENABLED") == "false"
assert not config.get("OPENAI_API_KEY")

def call(path, body=None, key=None):
    headers = {"Authorization": "Bearer " + config["BIA_API_TOKEN"], "Content-Type": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request("http://127.0.0.1:8080" + path, headers=headers, data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(req, timeout=5) as r:
        return json.load(r)

def wait(check):
    for _ in range(100):
        value = check()
        if value:
            return value
        time.sleep(0.2)
    raise AssertionError("worker timed out")

now = datetime.datetime.now(datetime.timezone.utc)
offset = now.hour - 12
zone = "Etc/GMT" + ("+" if offset >= 0 else "-") + str(abs(offset))
prefix = "deploy-smoke-" + secrets.token_hex(6)
# Synthetic fixture. No messaging provider exists in this mode.
phone = "+55659" + str(secrets.randbelow(100000000)).zfill(8)
form = {"name": "Teste sintetico de publicacao", "phone": phone, "revenue": 0, "consent": True, "consent_text_version": "synthetic-test-v1", "channel": "gateway", "timezone": zone, "source": "deployment-smoke"}
assert call("/readyz")["status"] == "ready"
lead = call("/v1/forms", form, prefix + "-form")
assert lead["mode"] == "shadow" and lead["contact_status"] == "queued_for_simulation"
lead_id = lead["lead_id"]
try:
    wait(lambda: call("/v1/jobs/" + prefix + "-form")["status"] == "done")
    assert call("/v1/forms", form, prefix + "-form")["duplicate"]
    voice_path = "/v1/leads/" + lead_id + "/voice"
    call(voice_path, {"purpose": "schedule"}, prefix + "-voice")
    wait(lambda: call("/v1/voice/" + prefix + "-voice")["state"] == "simulated")
    assert call(voice_path, {"purpose": "schedule"}, prefix + "-voice")["duplicate"]
    assert call("/v1/leads/" + lead_id + "/availability")["status"] == "empty"
    metrics = call("/v1/metrics")
    assert metrics["sent"] == 0 and metrics["dead"] == 0, metrics
finally:
    call("/v1/events", {"id": prefix + "-stop", "lead_id": lead_id, "kind": "contact.opt_out", "at": datetime.datetime.now(datetime.timezone.utc).isoformat()})
    wait(lambda: call("/v1/leads/" + lead_id)["opted_out"])

result = {"mode": "shadow", "ready": True, "form_processed": True, "voice_simulated": True, "deduplication": True, "synthetic_contact_opted_out": True, "sent": metrics["sent"], "dead": metrics["dead"], "openai_requests": 0, "at": datetime.datetime.now(datetime.timezone.utc).isoformat()}
Path("/var/backups/arkom-bia/deployment-smoke.json").write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps(result))
