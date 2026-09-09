#!/usr/bin/env python3
"""Bounded, loopback-only full-Server comparison. See the investigation report.

Run in an isolated generic Linux container with locally built binaries in
/lab/bin and a new disposable /lab/out. No customer identities or endpoints.
"""
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import ssl
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path("/lab")
OUT = ROOT / "out"
BASE = {k: v for k, v in os.environ.items()
        if not k.startswith(("MINIO_", "SILO_", "CONSOLE_"))
        and k.lower() not in {"http_proxy", "https_proxy", "all_proxy", "no_proxy", "godebug"}}


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(url, opener=None, payload=None):
    headers = {"Origin": f"http://{urllib.parse.urlparse(url).netloc}"}
    if payload is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=None if payload is None else json.dumps(payload).encode(), headers=headers)
    try:
        response = (opener.open if opener else urllib.request.urlopen)(req, timeout=2)
    except urllib.error.HTTPError as err:
        response = err
    except (urllib.error.URLError, TimeoutError):
        return 0, {}, b""
    with response:
        return response.code, dict(response.headers), response.read(1 << 20)


def stop(proc):
    proc.terminate()
    try:
        proc.wait(timeout=4)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=2)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def login(console, ca):
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    status, _, raw = request(console + "/api/v1/login", client)
    details = json.loads(raw)
    rules = details.get("redirectRules", [])
    assert status == 200 and len(rules) == 1, (status, details)
    auth_url = rules[0]["redirect"]
    provider = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(ca))))
    status, headers, _ = request(auth_url, provider)
    callback = headers.get("Location", headers.get("location", ""))
    assert status == 302 and callback.startswith(console + "/oauth_callback?"), (status, callback)
    values = urllib.parse.parse_qs(urllib.parse.urlparse(callback).query)
    callback_status, _, _ = request(callback, client)
    status, _, _ = request(console + "/api/v1/login/oauth2/auth", client,
                           {"code": values["code"][0], "state": values["state"][0]})
    buckets_status, _, _ = request(console + "/api/v1/buckets", client)
    # Do not record cookies, codes, JWTs, or the state value.
    return {"callback_status": callback_status, "login_status": status,
            "buckets_status": buckets_status, "session_cookie": any(c.name == "token" for c in jar)}


def run(name, binary, mode="normal", debug=None, tls13=False,
        expected=True, trusted=True, oidc=True, oauth=False, add=False):
    d = OUT / name
    (d / "certs/CAs").mkdir(parents=True, exist_ok=False)
    idp = d / "idp"
    idp.mkdir()
    (idp / "mode").write_text(mode)
    with (d / "fixture.jsonl").open("w") as events, (d / "fixture.stderr").open("w") as errors, (d / "server.log").open("w") as logs:
        fixture = subprocess.Popen([str(ROOT / "bin/fixture"), "-dir", str(idp), *(["-tls13"] if tls13 else [])], env=BASE, stdout=events, stderr=errors)
        server = None
        try:
            until = time.monotonic() + 5
            while not (idp / "url").is_file() and time.monotonic() < until:
                time.sleep(.05)
            assert (idp / "url").is_file(), "fixture did not initialize"
            url = (idp / "url").read_text() + "/.well-known/openid-configuration"
            if trusted:
                shutil.copyfile(idp / "ca.pem", d / "certs/CAs/lab.pem")
            sport, cport = port(), port()
            address = f"127.0.0.1:{sport}"
            api, console = "http://" + address, f"http://127.0.0.1:{cport}"
            password = secrets.token_urlsafe(24)
            env = dict(BASE, MINIO_ROOT_USER="local154", MINIO_ROOT_PASSWORD=password, MINIO_BROWSER="on")
            if debug:
                env["GODEBUG"] = debug
            if oidc:
                env.update(MINIO_IDENTITY_OPENID_CONFIG_URL=url,
                           MINIO_IDENTITY_OPENID_CLIENT_ID="local154",
                           MINIO_IDENTITY_OPENID_CLIENT_SECRET="local154-placeholder",
                           MINIO_IDENTITY_OPENID_REDIRECT_URI=console + "/oauth_callback")
            server = subprocess.Popen([str(ROOT / "bin" / binary), "--config-dir", str(d / "config"), "--certs-dir", str(d / "certs"), "server", "--address", address, "--console-address", f"127.0.0.1:{cport}", str(d / "data")], env=env, stdout=logs, stderr=subprocess.STDOUT)
            until = time.monotonic() + 15
            while time.monotonic() < until:
                assert server.poll() is None, "Server exited; inspect its local log"
                status, _, _ = request(api + "/minio/health/cluster")
                if status == 200 and request(console)[0] == 200:
                    break
                if not expected and (d / "server.log").read_text().count("Waiting for OpenID") >= 2:
                    break
                time.sleep(.1)
            result = {"case": name, "binary": binary, "mode": mode, "godebug": debug, "tls13": tls13,
                      "cluster": request(api + "/minio/health/cluster")[0],
                      "ready": request(api + "/minio/health/ready")[0], "console": request(console)[0]}
            assert result["cluster"] == (200 if expected else 503), result
            aenv = dict(BASE, LAB_SERVER=address, LAB_USER="local154", LAB_PASSWORD=password, LAB_OIDC_URL=url)
            if expected:
                admin = subprocess.run([str(ROOT / "bin/admin-check")], env=aenv, capture_output=True, text=True, timeout=7)
                result["admin_list_ok"] = admin.returncode == 0
                assert result["admin_list_ok"], admin.stdout
            curl = subprocess.run(["curl", "--cacert", str(idp / "ca.pem"), "--http2", "--max-time", "3", "-sS", "-o", "/dev/null", "-w", "%{http_code} %{http_version}", url], env=BASE, capture_output=True, text=True, timeout=5)
            result["curl"] = {"exit": curl.returncode, "status_protocol": curl.stdout}
            if add:
                attempt = subprocess.run([str(ROOT / "bin/admin-check"), "add"], env=aenv, capture_output=True, text=True, timeout=7)
                result["add_ok"] = attempt.returncode == 0
                result["add_reset"] = "connection reset by peer" in attempt.stdout
                assert result["add_ok"] == binary.startswith("candidate"), result
            if oauth:
                result["oauth"] = login(console, idp / "ca.pem")
                assert result["oauth"]["login_status"] == 204 and result["oauth"]["buckets_status"] == 200, result
                for bad in ("bad-signature", "bad-audience"):
                    (idp / "mode").write_text(bad)
                    result[bad] = login(console, idp / "ca.pem")
                    assert result[bad]["login_status"] >= 400 and result[bad]["buckets_status"] >= 400, result
            # Public handshake metadata only; no authorization parameters.
            result["events"] = [json.loads(line) for line in (d / "fixture.jsonl").read_text().splitlines()]
            (d / "result.json").write_text(json.dumps(result, indent=2) + "\n")
            print(json.dumps({k: v for k, v in result.items() if k != "events"}), flush=True)
        finally:
            if server is not None:
                stop(server)
            stop(fixture)


if __name__ == "__main__":
    run("old126-normal", "old-go126")
    run("old127-normal", "old-go127")
    run("old126-compat", "old-go126", "reject-mlkem", "tlsmlkem=0")
    run("old127-compat", "old-go127", "reject-mlkem", "tlsmlkem=0", expected=False)
    run("head127-compat", "head-go127", "reject-mlkem", "tlsmlkem=0", expected=False)
    run("candidate127-compat-login", "candidate-go127", "reject-mlkem", "tlsmlkem=0", oauth=True)
    run("candidate127-no-optout", "candidate-go127", "reject-mlkem", expected=False)
    run("candidate127-tls13-login", "candidate-go127", tls13=True, oauth=True)
    run("candidate127-untrusted", "candidate-go127", trusted=False, expected=False)
    run("candidate127-mldsa", "candidate-go127", "reject-mldsa", "tlsmlkem=0", expected=False)
    run("head127-add", "head-go127", "reject-mlkem", "tlsmlkem=0", oidc=False, add=True)
    run("candidate127-add", "candidate-go127", "reject-mlkem", "tlsmlkem=0", oidc=False, add=True)
