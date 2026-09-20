"""Functional checks for the local Compose stack; no external Python dependencies."""
import json
import subprocess
import time
import urllib.error
import urllib.request

BASE = "http://localhost:8088"


def request(path, method="GET", data=None):
    req = urllib.request.Request(BASE + path, data=data, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()


def stats():
    status, body = request("/api/stats")
    assert status == 200, (status, body)
    return json.loads(body)


def wait_health(name, healthy):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if any(b["name"] == name and b["healthy"] == healthy for b in stats()["backends"]):
            return
        time.sleep(0.6)
    raise AssertionError(f"{name} did not become healthy={healthy}")


def traffic(n):
    backends = []
    for _ in range(n):
        time.sleep(0.6)  # Stay below the public demo's 2 requests/second limit.
        status, body = request("/demo/request", "POST", b"")
        assert status == 200, (status, body)
        backends.append(json.loads(body)["backend"])
    return backends


status, page = request("/")
assert status == 200 and 'id="send-demo"' in page
snapshot = stats()
assert all(b["url"] == "Private upstream" for b in snapshot["backends"])
assert set(traffic(6)) == {"alpha", "bravo", "charlie"}
for path in ("/demo/fail", "/demo/recover", "/orders", "/readyz", "/healthz"):
    assert request(path)[0] == 404, path
assert request("/demo/fail", "POST", b"")[0] == 404
assert request("/demo/request?delay=30s", "POST", b"")[0] == 400
assert request("/demo/request", "POST", b"payload")[0] == 400
assert "relay_requests_total" in request("/metrics/raw")[1]

# Verify only the edge service publishes ports on the host.
config = json.loads(subprocess.check_output(["docker", "compose", "config", "--format", "json"]))
assert all(not svc.get("ports") for name, svc in config["services"].items() if name != "caddy")
subprocess.run(["docker", "compose", "stop", "bravo"], check=True)
try:
    wait_health("bravo", False)
    selected = traffic(6)
    assert selected.count("alpha") == 3 and selected.count("charlie") == 3, selected
finally:
    subprocess.run(["docker", "compose", "start", "bravo"], check=True)
wait_health("bravo", True)
assert set(traffic(6)) == {"alpha", "bravo", "charlie"}
print("PASS: dashboard, public isolation, fixed demo requests, round robin, outage and recovery")
