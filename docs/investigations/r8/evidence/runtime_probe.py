"""Probe the compiled SILO CLI on disposable loopback-only single-disk servers."""
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

binary = Path(sys.argv[1]).resolve()
expected_rejection = sys.argv[2] == 'fixed'
output_dir = Path(sys.argv[3]).resolve()
output_dir.mkdir(parents=True, exist_ok=True)
results = []
for source in ('flag', 'environment'):
    with socket.socket() as reservation:
        reservation.bind(('127.0.0.1', 0))
        port = reservation.getsockname()[1]
    data_dir = tempfile.mkdtemp(prefix=f'r8-{source}-', dir=output_dir)
    env = {k: v for k, v in os.environ.items() if not k.startswith(('MINIO_', 'SILO_'))}
    env.update(MINIO_ROOT_USER='r8localtest', MINIO_ROOT_PASSWORD='r8-local-disposable-test-only', MINIO_BROWSER='off')
    args = [str(binary), 'server', f'--address=127.0.0.1:{port}', '--console-address=127.0.0.1:0', '--idle-timeout=2s']
    if source == 'flag':
        args.append('--read-header-timeout=100ms')
    else:
        env['MINIO_READ_HEADER_TIMEOUT'] = '100ms'
    args.append(data_dir)
    with (output_dir / f'{source}-server.log').open('w') as server_log:
        proc = subprocess.Popen(args, env=env, stdout=server_log, stderr=subprocess.STDOUT)
        try:
            url = f'http://127.0.0.1:{port}/minio/health/live'
            started = time.monotonic()
            while True:
                if proc.poll() is not None:
                    raise RuntimeError(f'{source}: server exited {proc.returncode}; see its log')
                try:
                    with urllib.request.urlopen(url, timeout=1) as resp:
                        if resp.status == 200:
                            break
                except Exception:
                    pass
                if time.monotonic() - started > 30:
                    raise RuntimeError(f'{source}: startup timed out')
                time.sleep(0.1)
            with socket.create_connection(('127.0.0.1', port), timeout=2) as conn:
                conn.settimeout(3)
                conn.sendall(b'GET /minio/health/live HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\nX-Slow: ')
                before = time.monotonic()
                time.sleep(0.4)
                try:
                    conn.sendall(b'done\r\n\r\n')
                    data = conn.recv(4096)
                except (BrokenPipeError, ConnectionResetError):
                    data = b''
                rejected = not data
                status_line = data.split(b'\r\n', 1)[0].decode('ascii', 'replace')
                elapsed = time.monotonic() - before
            with urllib.request.urlopen(url, timeout=2) as resp:
                alive = resp.status == 200
            result = dict(source=source, header_timeout_ms=100, idle_timeout_ms=2000,
                          header_completion_delay_ms=400, rejected=rejected, status=status_line,
                          elapsed_seconds=round(elapsed, 3), still_alive=alive)
            results.append(result)
            if rejected != expected_rejection or not alive:
                raise AssertionError(result)
        finally:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)
report = dict(binary=str(binary), binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),
              expected_rejection=expected_rejection, cases=results)
print(json.dumps(report, indent=2))
