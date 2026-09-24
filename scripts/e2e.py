#!/usr/bin/env python3
"""Exercise a running local Go gateway. Paid phases are explicit and bounded.

Uses logged development emails (RESEND_API_KEY must be unset), never sends mail.
Credentials and run state stay in a private gitignored directory.
"""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--url', default='http://localhost:14000')
p.add_argument('--env-file', default='.env.e2e.local')
p.add_argument('--container', default='vgw-e2e-gateway')
p.add_argument('--state-dir', default='.dev/e2e')
p.add_argument('--phase', choices=['shell', 'abr', 'live'], default='shell')
a = p.parse_args()
env = dict(line.split('=', 1) for line in Path(a.env_file).read_text().splitlines() if line and not line.startswith('#') and '=' in line)
if env.get('RESEND_API_KEY'):
    raise SystemExit('Use development logged email mode for this harness.')
state_dir = Path(a.state_dir)
state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
state_file = state_dir / 'session.json'
state = json.loads(state_file.read_text()) if state_file.exists() else {}
cookies = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))

def save():
    fd = os.open(state_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump(state, f)

def http(method, path, body=None, headers=None, expect=(200,), timeout=45):
    target = path if path.startswith(('http://', 'https://')) else a.url.rstrip('/') + path
    h = {'User-Agent': 'curl/8.0 gateway-e2e'}
    h.update(headers or {})
    if body is not None and not isinstance(body, bytes):
        body = json.dumps(body).encode()
        h['Content-Type'] = 'application/json'
    req = urllib.request.Request(target, data=body, method=method, headers=h)
    try:
        resp = client.open(req, timeout=timeout)
    except urllib.error.HTTPError as err:
        resp = err
    with resp:
        raw = resp.read()
        status = resp.status
    if status not in expect:
        # Do not print response bodies or signed URLs: they can contain credentials.
        raise RuntimeError(f'{method} {urllib.parse.urlsplit(target).path}: HTTP {status}, expected {expect}')
    try:
        return json.loads(raw)
    except (ValueError, UnicodeDecodeError):
        return raw

def logged_text(email, subject):
    result = subprocess.run(['docker', 'logs', a.container], capture_output=True, text=True, check=True)
    for line in reversed((result.stdout + result.stderr).splitlines()):
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if row.get('to') == email and row.get('subject') == subject:
            return row['text']
    raise RuntimeError('Matching development email missing from gateway logs')

def auth():
    return {'Authorization': 'Bearer ' + state['key']}

def pass_(message):
    print('PASS ' + message, flush=True)

def fixture():
    path = state_dir / 'input.mp4'
    if not path.exists():
        subprocess.run(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=15', '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000', '-t', '3', '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-preset', 'ultrafast', '-c:a', 'aac', '-movflags', '+faststart', '-y', str(path)], check=True)
    return path

def shell():
    for path in ['/', '/portal/', '/admin/', '/openapi.json']:
        http('GET', path)
    pass_('embedded apps and OpenAPI')
    health = http('GET', '/health')
    print('HEALTH', json.dumps({k: v['status'] for k, v in health['checks'].items()}), flush=True)
    http('POST', '/api/v1/abr', {'input_url': 'https://example.invalid/input.mp4'}, expect=(401,))
    http('GET', '/api/admin/waitlist', expect=(401,))
    pass_('unauthenticated paid/admin requests rejected')
    if 'key' not in state:
        state['email'] = 'e2e-' + uuid.uuid4().hex + '@example.invalid'
        http('POST', '/api/public/waitlist', {'name': 'Local E2E', 'email': state['email']})
        text = logged_text(state['email'], 'Verify your Livepeer Video Gateway signup')
        token = re.search(r'token=([A-Za-z0-9_-]+)', text).group(1)
        http('GET', '/api/public/verify?token=' + token)
        http('GET', '/api/public/verify?token=' + token, expect=(400,))
        admin = {'X-Admin-Token': env['ADMIN_TOKEN']}
        rows = http('GET', '/api/admin/waitlist', headers=admin)['items']
        row = next(x for x in rows if x['email'] == state['email'])
        http('POST', '/api/admin/waitlist/' + row['id'] + '/approve', headers=admin)
        text = logged_text(state['email'], 'Your Livepeer Video Gateway API key')
        state['key'] = re.search(r'sk-[A-Za-z0-9_-]+', text).group(0)
        save()
    pass_('signup, one-time email verification, admin approval, key issuance')
    http('POST', '/api/portal/login', {'apiKey': state['key']})
    account = http('GET', '/api/portal/account')
    assert account['email'] == state['email']
    # Remove only this harness's abandoned revocation probes from an interrupted run.
    for old_key in http('GET', '/api/portal/api-keys')['keys']:
        if old_key.get('label') == 'E2E revocation probe' and not old_key.get('revoked_at'):
            http('DELETE', '/api/portal/api-keys/' + old_key['id'])
    temporary = http('POST', '/api/portal/api-keys', {'label': 'E2E revocation probe'})
    temporary_auth = {'Authorization': 'Bearer ' + temporary['plaintext_key']}
    http('GET', '/api/v1/capabilities', headers=temporary_auth)
    if state.get('input_url'):
        http('DELETE', '/api/v1/abr/objects', {'object_url': state['input_url']}, temporary_auth, expect=(403,))
        pass_('object namespace isolated between API keys')
    http('DELETE', '/api/portal/api-keys/' + temporary['key']['id'])
    http('GET', '/api/v1/capabilities', headers=temporary_auth, expect=(401,))
    pass_('API-key minting and revocation')
    http('POST', '/api/portal/logout')
    http('GET', '/api/portal/account', expect=(401,))
    pass_('portal cookie login, account, logout and revocation')
    catalog = http('GET', '/api/v1/capabilities', headers=auth())
    print('CATALOG', [(x['capability'], x['offering'], x['protocol']) for x in catalog.get('data') or []], flush=True)
    metrics = http('GET', '/metrics', headers={'Authorization': 'Bearer ' + env['METRICS_TOKEN']})
    assert b'video_gateway_' in metrics
    pass_('authenticated metrics')
    data = fixture().read_bytes()
    if not state.get('input_url'):
        upload = http('POST', '/api/v1/abr/upload-url', {'filename': 'e2e.mp4', 'content_type': 'video/mp4'}, auth())
        http('PUT', upload['upload_url'], data, {'Content-Type': 'video/mp4'}, expect=(200, 204))
        state['input_url'] = upload['object_url']
        save()
    assert http('GET', state['input_url']) == data
    pass_('presigned upload and public storage byte-for-byte roundtrip')

def abr():
    body = {'input_url': state['input_url'], 'preset': 'abr-mobile', 'estimated_input_seconds': 3}
    state.setdefault('abr_request', 'e2e-abr-' + uuid.uuid4().hex)
    save()
    headers = {**auth(), 'Idempotency-Key': state['abr_request']}
    job = http('POST', '/api/v1/abr', body, headers, expect=(202,))['job']
    state['abr_id'] = job['id']; save()
    replay = http('POST', '/api/v1/abr', body, headers, expect=(202,))['job']
    assert replay['id'] == job['id']
    http('POST', '/api/v1/abr', {**body, 'preset': 'abr-standard'}, headers, expect=(409,))
    pass_('ABR submit, idempotent replay, changed-body rejection')
    last = None
    for _ in range(120):
        job = http('GET', '/api/v1/abr/' + state['abr_id'], headers=auth())['job']
        view = (job['status'], job.get('accounting_state'), job.get('phase'), job.get('error_code'))
        if view != last: print('ABR', state['abr_id'], view, flush=True); last = view
        if job['status'] in ('succeeded', 'failed'): break
        time.sleep(2)
    state['abr_result'] = job; save()
    if job['status'] != 'succeeded': raise RuntimeError('ABR did not succeed; inspect local journal using saved operation ID')
    assert job['work_unit'] == 'video-frame-megapixel' and job['actual_units'] > 0
    master = http('GET', job['master_playlist_url'])
    assert b'#EXTM3U' in master
    for variant in job['renditions']:
        playlist = http('GET', variant['playlist_url'])
        assert b'#EXTM3U' in playlist
        media = next(x for x in playlist.decode().splitlines() if x and not x.startswith('#'))
        assert http('GET', urllib.parse.urljoin(variant['playlist_url'], media))
    pass_('ABR terminal signed accounting and HLS artifacts')

def live():
    state.setdefault('live_request', 'e2e-live-' + uuid.uuid4().hex); save()
    headers = {**auth(), 'Idempotency-Key': state['live_request']}
    session = http('POST', '/api/v1/live', {'name': 'Local E2E bounded publish'}, headers)['session']
    state['live_id'] = session['id']; save()
    stream_key = session['ingest'].get('stream_key')
    proc = None
    try:
        replay = http('POST', '/api/v1/live', {'name': 'Local E2E bounded publish'}, headers)['session']
        assert replay['id'] == session['id']
        for _ in range(45):
            current = http('GET', '/api/v1/live/' + session['id'], headers=auth())['session']
            assert 'stream_key' not in current['ingest']
            if current['status'] in ('live', 'failed', 'ended'): break
            time.sleep(2)
        print('LIVE admission', current['status'], current.get('accounting_state'), flush=True)
        if current['status'] != 'live': raise RuntimeError('Live admission did not become ready')
        assert stream_key
        # Keep runner credentials out of console logs. Publishing is bounded.
        target = session['ingest']['rtmp_url'] + '/' + stream_key
        media_log = open(state_dir / 'ffmpeg.log', 'w')
        proc = subprocess.Popen(['ffmpeg', '-hide_banner', '-loglevel', 'warning', '-re', '-stream_loop', '-1', '-i', str(fixture()), '-t', '35', '-c', 'copy', '-f', 'flv', target], stdout=subprocess.DEVNULL, stderr=media_log)
        playing = False
        for _ in range(30):
            time.sleep(1)
            try:
                master = http('GET', current['playback']['hls_url'], timeout=4)
                if not isinstance(master, bytes) or b'#EXTM3U' not in master: continue
                variant = next(x for x in master.decode().splitlines() if x and not x.startswith('#'))
                variant_url = urllib.parse.urljoin(current['playback']['hls_url'], variant)
                playlist = http('GET', variant_url, timeout=4)
                segments = [x for x in playlist.decode().splitlines() if x and not x.startswith('#')]
                if segments and http('GET', urllib.parse.urljoin(variant_url, segments[0]), timeout=4):
                    playing = True; break
            except (RuntimeError, OSError, StopIteration): pass
        assert playing, 'Live HLS never yielded a media segment'
        pass_('gateway RTMP relay to real runner with playable HLS segments')
    finally:
        try:
            http('DELETE', '/api/v1/live/' + session['id'], headers=auth(), expect=(202,))
        finally:
            if proc:
                proc.terminate()
                try: proc.wait(timeout=5)
                except subprocess.TimeoutExpired: proc.kill(); proc.wait()
                media_log.close()
    for _ in range(60):
        current = http('GET', '/api/v1/live/' + session['id'], headers=auth())['session']
        if current['status'] in ('ended', 'failed'): break
        time.sleep(2)
    state['live_result'] = current; save()
    print('LIVE terminal', current['status'], current.get('accounting_state'), 'units', current.get('actual_units'), flush=True)
    assert current['status'] == 'ended' and current['accounting_state'] == 'broker_settled' and current['actual_units'] > 0
    http('DELETE', '/api/v1/live/' + session['id'], headers=auth(), expect=(202,))
    pass_('live idempotent stop and signed measured settlement')

try:
    {'shell': shell, 'abr': abr, 'live': live}[a.phase]()
except Exception as err:
    print('FAIL', type(err).__name__, str(err) if isinstance(err, (RuntimeError, AssertionError)) else '(details withheld to protect credentials)', flush=True)
    raise SystemExit(1)
