#!/usr/bin/env python3
"""Bounded four-container restart/readback acceptance for pgsty/silo#116."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import time
import uuid
from concurrent.futures import ThreadPoolExecutor

import boto3
from botocore.config import Config

ROOT = None
MCLI = None
CURRENT_BINARY = None
SOURCE_REVISION = None
IMAGES = {
    '0806': 'pgsty/silo:RELEASE.2026-08-06T00-00-00Z',
    '0903': 'pgsty/silo:RELEASE.2026-09-03T13-18-01Z',
    'current': 'pgsty/d12a:build',
}


def docker(*args, timeout=40, check=True):
    return subprocess.run(['docker', *args], check=check, capture_output=True, text=True, timeout=timeout)


class Deadline(BaseException):
    pass


def bounded(seconds, operation):
    def expired(*_):
        raise Deadline(f'hard deadline of {seconds}s exceeded')
    old = signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, seconds)
    try:
        return operation(time.monotonic() + seconds)
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, old)


def remaining(deadline):
    value = deadline - time.monotonic()
    if value <= 0:
        raise Deadline('absolute deadline exceeded')
    return value


def run(version):
    image_info = json.loads(docker('image', 'inspect', IMAGES[version]).stdout)[0]
    runid = f'silo-v1-{version}-{uuid.uuid4().hex[:8]}'
    out = ROOT / runid
    out.mkdir(mode=0o700)
    user, password = 'local116', secrets.token_urlsafe(24)
    envfile = out / 'credentials.env'
    envfile.write_text(f'MINIO_ROOT_USER={user}\nMINIO_ROOT_PASSWORD={password}\nMINIO_CI_CD=1\nMINIO_BROWSER=off\nGOMAXPROCS=2\n')
    envfile.chmod(0o600)
    env = {k: v for k, v in os.environ.items() if not k.startswith(('MINIO_', 'SILO_', 'MC_'))
           and k.lower() not in {'http_proxy', 'https_proxy', 'all_proxy', 'no_proxy'}}
    nodes = [f'{runid}-n{i}' for i in range(4)]
    sockets = [socket.socket() for _ in nodes]
    for sock in sockets:
        sock.bind(('127.0.0.1', 0))
    ports = [sock.getsockname()[1] for sock in sockets]
    for sock in sockets:
        sock.close()
    volumes = [n + '-data' for n in nodes]
    endpoints, ledger = [], []
    result = {'runid': runid, 'version': version, 'image': IMAGES[version],
              'image_id': image_info['Id'], 'platform': image_info['Os']+'/'+image_info['Architecture'],
              'nodes': 4,
              'drives': 4, 'filesystem': 'Linux tmpfs named volumes, held mounted across server restarts',
              'drive_bytes': 268435456, 'phases': [], 'status': 'RUNNING'}
    if version == 'current':
        result['binary_sha256'] = hashlib.sha256(CURRENT_BINARY.read_bytes()).hexdigest()
        result['source_revision'] = SOURCE_REVISION
    bucket = 'canary-' + uuid.uuid4().hex[:10]

    def save(event=None):
        if event is not None:
            result['phases'].append(event)
            compact = {k: (len(v) if k in ('transient_errors', 'errors') else v) for k, v in event.items()}
            print(json.dumps({'version': version, **compact}), flush=True)
        (out / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        (out / 'acknowledged.json').write_text(json.dumps(ledger, indent=2) + '\n')

    def parallel(fn, values):
        with ThreadPoolExecutor(max_workers=4) as pool:
            return list(pool.map(fn, values))

    def client(i, deadline):
        timeout = remaining(deadline)
        return boto3.client('s3', endpoint_url=endpoints[i], aws_access_key_id=user,
                            aws_secret_access_key=password, region_name='us-east-1',
                            config=Config(proxies={}, signature_version='s3v4', s3={'addressing_style': 'path'},
                                          retries={'total_max_attempts': 1}, connect_timeout=timeout,
                                          read_timeout=timeout, request_checksum_calculation='when_required',
                                          response_checksum_validation='when_required'))

    def admin_gate(origin, phase):
        def check(deadline):
            first = [None] * 4
            while True:
                states = []
                for i in range(4):
                    try:
                        p = subprocess.run([str(MCLI), '--config-dir', str(out / 'mcli'), '--json',
                                            'admin', 'info', f'n{i}'], env=env, capture_output=True,
                                           text=True, timeout=min(5, remaining(deadline)))
                        info = json.loads(p.stdout)['info']
                        servers = info['servers']
                        ok = len(servers) == 4 and all(s.get('state') == 'online' and s.get('drives')
                             and all(d.get('state') == 'ok' for d in s['drives'])
                             and all(v == 'online' for v in s.get('network', {}).values()) for s in servers)
                        if ok:
                            (out / f'{phase}-admin-{i}.json').write_text(json.dumps(info, indent=2) + '\n')
                            if first[i] is None:
                                first[i] = round(time.monotonic() - origin, 3)
                        states.append(ok)
                    except (Exception,):
                        states.append(False)
                if all(states):
                    event = {'phase': phase + '-admin', 'seconds_from_start': round(time.monotonic()-origin, 3),
                             'first_online_by_coordinator': first}
                    save(event)
                    return time.monotonic()
                time.sleep(min(.25, remaining(deadline)))
        return bounded(90, check)

    def canary(phase, origin, admin_time, setup=False):
        def check(deadline):
            started = time.monotonic()
            first_put, first_reads = [None] * 4, [None] * 4
            errors, attempt, setup_done = [], 0, not setup
            result['active_canary'] = {'phase': phase, 'errors': errors}
            while True:
                attempt += 1
                remaining(deadline)
                if not setup_done:
                    try:
                        try:
                            client(0, deadline).create_bucket(Bucket=bucket)
                        except Exception as e:
                            if 'BucketAlreadyOwnedByYou' not in str(e):
                                raise
                        client(0, deadline).put_bucket_versioning(Bucket=bucket, VersioningConfiguration={'Status': 'Enabled'})
                        setup_done = True
                    except Exception as e:
                        errors.append({'attempt': attempt, 'operation': 'setup', 'error': str(e)[:250]})
                        time.sleep(min(.25, remaining(deadline)))
                        continue
                acked = []
                for i in range(4):
                    key = f'{phase}-attempt-{attempt}-node-{i}'
                    payload = (key + '\n').encode() * 16384
                    try:
                        vid = client(i, deadline).put_object(Bucket=bucket, Key=key, Body=payload)['VersionId']
                        entry = {'phase': phase, 'key': key, 'version': vid, 'bytes': len(payload),
                                 'sha256': hashlib.sha256(payload).hexdigest(), 'writer': i,
                                 'ack_seconds_from_start': round(time.monotonic()-origin, 3)}
                        ledger.append(entry)
                        save()  # Persist every acknowledged write, including failed rounds.
                        acked.append(entry)
                        if first_put[i] is None:
                            first_put[i] = round(time.monotonic()-admin_time, 3)
                    except Exception as e:
                        errors.append({'attempt': attempt, 'operation': 'put', 'node': i, 'error': str(e)[:250]})
                reads = 0
                for i in range(4):
                    own_reads = 0
                    for entry in acked:
                        try:
                            verify(i, entry, deadline)
                            reads += 1
                            own_reads += 1
                        except Exception as e:
                            errors.append({'attempt': attempt, 'operation': 'get', 'node': i,
                                           'key': entry['key'], 'error': str(e)[:250]})
                    if own_reads == 4 and first_reads[i] is None:
                        first_reads[i] = round(time.monotonic()-admin_time, 3)
                remaining(deadline)
                if len(acked) == 4 and reads == 16:
                    result.pop('active_canary', None)
                    save({'phase': phase + '-canary', 'attempts': attempt, 'puts': 4, 'gets': 16,
                          'hard_deadline_seconds': 60, 'gate_seconds': round(time.monotonic()-started, 3),
                          'seconds_from_start': round(time.monotonic()-origin, 3),
                          'first_put_seconds_after_admin_by_coordinator': first_put,
                          'first_four_reads_seconds_after_admin_by_coordinator': first_reads, 'transient_errors': errors})
                    return time.monotonic()
                (out / f'{phase}-canary-errors.json').write_text(json.dumps(errors, indent=2) + '\n')
                if attempt == 1:
                    print(json.dumps({'version': version, 'phase': phase, 'first_attempt_errors': errors}), flush=True)
                time.sleep(min(.25, remaining(deadline)))
        return bounded(60, check)

    def verify(i, entry, deadline):
        got = client(i, deadline).get_object(Bucket=bucket, Key=entry['key'], VersionId=entry['version'])
        try:
            data = got['Body'].read()
        finally:
            got['Body'].close()
        assert len(data) == entry['bytes'] and hashlib.sha256(data).hexdigest() == entry['sha256'], entry['key']
        assert got.get('VersionId') == entry['version'], entry['key']
        remaining(deadline)

    def readback(label, origin):
        def check(deadline):
            started = time.monotonic()
            errors = []
            for entry in ledger:
                for i in range(4):
                    try:
                        verify(i, entry, deadline)
                    except Exception as e:
                        errors.append({'key': entry['key'], 'node': i, 'error': str(e)[:250]})
            save({'phase': label, 'started_seconds_after_canary': round(started-origin, 3),
                  'seconds_after_canary': round(time.monotonic()-origin, 3),
                  'objects': len(ledger), 'reads': len(ledger)*4, 'errors': errors})
            return errors
        return bounded(60, check)

    try:
        docker('network', 'create', runid)
        for volume in volumes:
            docker('volume', 'create', '--driver', 'local', '--opt', 'type=tmpfs',
                   '--opt', 'device=tmpfs', '--opt', 'o=size=256m', volume)
        mounts = [arg for i, v in enumerate(volumes) for arg in ('--mount', f'type=volume,source={v},target=/keep/{i}')]
        docker('run', '-d', '--pull=never', '--network', 'none', '--name', runid + '-keeper',
               *mounts, '--entrypoint', 'sleep', 'alpine:3.23', '1800')
        urls = [f'http://{n}:9000/data' for n in nodes]
        def create(i):
            extra = []
            if version == 'current':
                extra = ['--mount', f'type=bind,source={CURRENT_BINARY},target=/lab/silo,readonly',
                         '--entrypoint', '/lab/silo']
            docker('create', '--pull=never', '--name', nodes[i], '--network', runid,
                   '--hostname', nodes[i], '--cpus', '2', '--memory', '3g', '--env-file', str(envfile),
                   '--mount', f'type=volume,source={volumes[i]},target=/data',
                   '-p', f'127.0.0.1:{ports[i]}:9000', *extra, image_info['Id'], 'server', '--address', ':9000',
                   '--console-address', ':9001', *urls)
        parallel(create, range(4))
        start = time.monotonic()
        parallel(lambda n: docker('start', n), nodes)
        for i, n in enumerate(nodes):
            endpoint = 'http://' + docker('port', n, '9000/tcp').stdout.strip()
            endpoints.append(endpoint)
            env[f'MC_HOST_n{i}'] = endpoint.replace('http://', f'http://{user}:{password}@')
        admin = admin_gate(start, 'startup')
        canary('startup', start, admin, setup=True)
        parallel(lambda n: docker('stop', '-t', '10', n), nodes)
        start = time.monotonic()
        parallel(lambda n: docker('start', n), nodes)
        admin = admin_gate(start, 'full-restart')
        gate = canary('full-restart', start, admin)
        read_errors = []
        for after in (15, 30, 60):
            time.sleep(max(0, gate + after - time.monotonic()))
            read_errors.extend(readback(f'readback-{after}s', gate))
        assert not read_errors, 'acknowledged object readback failure; see result.json'
        docker('stop', '-t', '10', nodes[3])
        def outage(deadline):
            verify(0, ledger[0], deadline)
            payload = b'acknowledged with one Linux node offline' * 16384
            key = 'one-node-outage'
            vid = client(0, deadline).put_object(Bucket=bucket, Key=key, Body=payload)['VersionId']
            entry = {'phase': 'outage', 'key': key, 'version': vid, 'bytes': len(payload),
                     'sha256': hashlib.sha256(payload).hexdigest(), 'writer': 0}
            ledger.append(entry)
            save()
            for i in range(3):
                verify(i, entry, deadline)
            save({'phase': 'one-node-outage', 'existing_read': True, 'put': True, 'readers': 3})
        bounded(60, outage)
        start = time.monotonic()
        docker('start', nodes[3])
        admin = admin_gate(start, 'rejoin')
        gate = canary('rejoin', start, admin)
        assert not readback('final-readback', gate)
        result['status'] = 'PASS'
    except BaseException as e:
        result['status'] = 'FAIL'
        result['error'] = f'{type(e).__name__}: {e}'
        raise
    finally:
        save()
        for n in nodes:
            log = docker('logs', n, check=False)
            (out / (n + '.log')).write_text(log.stdout + log.stderr)
            docker('rm', '-f', n, check=False)
        docker('rm', '-f', runid + '-keeper', check=False)
        for v in volumes:
            docker('volume', 'rm', v, check=False)
        docker('network', 'rm', runid, check=False)
        envfile.unlink(missing_ok=True)
        print(json.dumps({'version': version, 'status': result['status'], 'evidence': str(out)}), flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('versions', nargs='+', choices=list(IMAGES))
    parser.add_argument('--output', type=Path, required=True, help='directory for retained evidence')
    parser.add_argument('--mcli', default=shutil.which('mcli'), help='native mcli executable')
    parser.add_argument('--current-binary', type=Path, help='Linux binary matching the Docker architecture')
    parser.add_argument('--source-revision', help='Git revision of --current-binary')
    parser.add_argument('--current-image', default=IMAGES['current'], help='cached Linux base image for current binary')
    args = parser.parse_args()
    if not args.mcli or not Path(args.mcli).is_file():
        parser.error('--mcli must point to an executable file')
    if 'current' in args.versions and (not args.current_binary or not args.current_binary.is_file()):
        parser.error('current requires --current-binary')
    ROOT = args.output.resolve()
    ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    MCLI = Path(args.mcli).resolve()
    CURRENT_BINARY = args.current_binary.resolve() if args.current_binary else None
    SOURCE_REVISION = args.source_revision
    IMAGES['current'] = args.current_image
    for version in args.versions:
        run(version)
