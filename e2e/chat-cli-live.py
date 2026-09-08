#!/usr/bin/env python3
"""Opt-in real CLI acceptance. Creates only uniquely named QA data on Dev2.

Requires an authenticated owner profile and a freshly built crewship binary.
All chat/workspace operations go through the CLI. Only account-setup redemption
uses HTTP, because that browser endpoint has no CLI equivalent. No secrets are
printed. On success the test workspace is deleted and test sessions revoked;
on failure a private state directory is retained for diagnosis/cleanup.
"""
import argparse
import concurrent.futures
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--server', required=True)
    parser.add_argument('--profile', required=True)
    parser.add_argument('--report', required=True)
    args = parser.parse_args()
    if args.server.rstrip('/') not in ('http://localhost:8082', 'http://127.0.0.1:8082',
                                       'https://crewship-dev2.unifylab.cz'):
        parser.error('This live test is restricted to Dev2.')
    run_id = uuid.uuid4().hex[:12]
    private = Path(tempfile.mkdtemp(prefix='crewship-chat-cli-'))
    report = {'server': args.server, 'run_id': run_id, 'checks': [], 'cleanup': False}
    workspace = ''
    actors = {'owner': None}

    def call(actor, *words, expect=None, raw=False, scoped=True, stdin=None):
        env = dict(os.environ)
        command = [args.binary, '--server', args.server, '--no-color', '-f', 'json']
        if actor == 'owner':
            command += ['--profile', args.profile]
        else:
            env = {k: v for k, v in env.items() if not k.startswith('CREWSHIP_')}
            env['CREWSHIP_CONFIG'] = str(actors[actor])
        if scoped and workspace:
            command += ['--workspace', workspace]
        command += list(words)
        result = subprocess.run(command, env=env, text=True, input=stdin,
                                capture_output=True, timeout=45)
        if expect is not None:
            assert result.returncode != 0, f'Unexpected success: {words[:3]}'
            failure = json.loads(result.stdout or result.stderr)
            assert failure['error']['status'] == expect, (words[:3], failure)
            return failure
        # Do not echo subprocess output: provisioning includes a one-time secret.
        assert result.returncode == 0, f'CLI failed ({result.returncode}): {words[:3]}; inspect private state'
        if raw or not result.stdout.strip():
            return result.stdout
        return json.loads(result.stdout)

    def passed(name):
        report['checks'].append(name)
        print('PASS', name, flush=True)

    def room(actor, *words, **kw):
        return call(actor, 'chat', 'room', *words, **kw)

    def rows(value, key):
        return value if isinstance(value, list) else value[key]

    def inbox(actor, conversation, state='unread'):
        value = call(actor, 'inbox', 'list', '--kind', 'message', '--state', state)
        return [i for i in rows(value, 'items')
                if conversation in json.dumps(i.get('metadata', {}))
                or conversation in str(i.get('source_id', ''))]

    def eventually(fn, timeout=12):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            value = fn()
            if value:
                return value
            time.sleep(.25)
        raise AssertionError('Timed out waiting for live projection')

    try:
        identity = call('owner', 'whoami', scoped=False)
        assert identity['server'].rstrip('/') == args.server.rstrip('/')
        ws = call('owner', 'workspace', 'create', '--name', 'Chat CLI QA '+run_id,
                  '--slug', 'chat-cli-qa-'+run_id, scoped=False)
        workspace = ws['id']
        report['workspace_id'] = workspace
        report['workspace_slug'] = ws['slug']
        (private / 'state.json').write_text(json.dumps(report))
        passed('Authenticated Dev2 profile and isolated workspace')
        for actor in ('bob', 'eve'):
            email = f'chat-cli-{actor}-{run_id}@example.invalid'
            invitation = call('owner', 'workspace', 'member', 'invite', email)
            assert invitation['created_user'] is True
            token = urllib.parse.parse_qs(urllib.parse.urlsplit(invitation['setup_url']).query)['token'][0]
            password = secrets.token_urlsafe(32)
            request = urllib.request.Request(args.server+'/api/v1/auth/reset',
                data=json.dumps({'token': token, 'new_password': password}).encode(),
                headers={'Content-Type': 'application/json'}, method='POST')
            with urllib.request.urlopen(request, timeout=30) as response:
                assert response.status == 200
            actors[actor] = private / (actor+'.yaml')
            call(actor, 'login', '--email', email, '--password-stdin',
                 stdin=password+'\n', scoped=False, raw=True)
        members = call('owner', 'workspace', 'member', 'list')
        user_ids = {actor: next(m['user_id'] for m in members
                    if m.get('email', m.get('user_email', '')) == f'chat-cli-{actor}-{run_id}@example.invalid')
                    for actor in ('bob', 'eve')}
        owner_id = next(m['user_id'] for m in members if m['role'] == 'OWNER')
        report['test_user_ids'] = user_ids
        (private / 'state.json').write_text(json.dumps(report))
        passed('Two real member accounts: setup and separate CLI login')

        dm = room('owner', 'direct', user_ids['bob'])
        same = room('bob', 'direct', owner_id)
        assert dm['id'] == same['id'] and dm['is_direct']
        did = dm['id']
        passed('Direct pair reopened from opposite participant has one identity')
        room('eve', 'get', did, expect=404)
        room('eve', 'messages', did, expect=404)
        room('eve', 'send', did, '-m', 'forbidden', '--client-id', 'denied', expect=404)
        room('owner', 'participants', 'add', did, user_ids['eve'], expect=400)
        passed('Private direct history/send denied to third user; fixed roster')
        first = room('owner', 'send', did, '-m', 'CLI QA hello — příliš žluťoučký', '--client-id', 'first')
        retry = room('owner', 'send', did, '-m', 'CLI QA hello — příliš žluťoučký', '--client-id', 'first')
        assert retry['id'] == first['id'] and retry['sequence'] == first['sequence']
        room('owner', 'send', did, '-m', 'different content', '--client-id', 'first', expect=409)
        history = room('bob', 'messages', did)['messages']
        assert [m['id'] for m in history] == [first['id']]
        passed('Unicode delivery, stable retry deduplication and conflict rejection')
        notices = eventually(lambda: inbox('bob', did))
        assert len(notices) == 1 and '/chat?conversation=' in json.dumps(notices)
        assert not inbox('eve', did)
        passed('Live outbox produces one recipient inbox item with canonical Chat link')
        room('bob', 'read', did, '--sequence', str(first['sequence']))
        eventually(lambda: not inbox('bob', did))
        assert room('bob', 'get', did)['unread_count'] == 0
        passed('Read cursor clears live inbox and unread count')
        room('bob', 'mute', did)
        second = room('owner', 'send', did, '-m', 'muted delivery', '--client-id', 'second')
        eventually(lambda: len(room('bob', 'messages', did)['messages']) == 2)
        time.sleep(1.5)
        assert not inbox('bob', did) and room('bob', 'get', did)['muted']
        room('bob', 'mute', did, '--muted=false')
        third = room('owner', 'send', did, '-m', 'unmuted delivery', '--client-id', 'third')
        eventually(lambda: inbox('bob', did))
        assert not room('bob', 'get', did)['muted']
        passed('Mute suppresses notification, preserves history; unmute restores delivery')
        reply = room('bob', 'send', did, '-m', 'reply from another login', '--client-id', 'reply')
        assert room('owner', 'messages', did)['messages'][-1]['author_user_id'] == user_ids['bob']
        newer = room('owner', 'messages', did, '--after-sequence', str(first['sequence']), '--limit', '1')
        older = room('owner', 'messages', did, '--before-sequence', str(third['sequence']), '--limit', '1')
        assert newer['messages'][0]['id'] == second['id'] and newer['has_more']
        assert older['messages'][0]['id'] == second['id']
        passed('Two-way human history and forward/backward message pagination')

        group = room('owner', 'create', '--title', 'CLI QA private group', '--kind', 'group', '--member', user_ids['bob'])
        gid = group['id']
        room('eve', 'get', gid, expect=404)
        room('owner', 'participants', 'add', gid, user_ids['eve'])
        assert len(rows(room('eve', 'participants', 'list', gid), 'participants')) == 3
        room('eve', 'send', gid, '-m', 'joined group', '--client-id', 'joined')
        room('owner', 'participants', 'remove', gid, user_ids['eve'])
        room('eve', 'get', gid, expect=404)
        room('eve', 'send', gid, '-m', 'after removal', '--client-id', 'removed', expect=404)
        passed('Private group membership grant and immediate revocation')
        channel = room('owner', 'create', '--title', 'CLI QA workspace channel', '--kind', 'channel')
        cid = channel['id']
        assert room('eve', 'get', cid)['access_scope'] == 'workspace'
        room('eve', 'send', cid, '-m', 'workspace participant', '--client-id', 'channel')
        page1 = room('owner', 'list', '--limit', '1')
        page2 = room('owner', 'list', '--limit', '1', '--offset', str(page1['next_offset']))
        assert page1['conversations'][0]['id'] != page2['conversations'][0]['id']
        assert did not in [c['id'] for c in room('eve', 'list')['conversations']]
        passed('Workspace channel access and ACL-filtered room pagination')

        def concurrent_send(index):
            return room('owner' if index % 2 else 'bob', 'send', did,
                        '-m', f'concurrent {index}', '--client-id', f'concurrent-{index}')
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            sent = list(pool.map(concurrent_send, range(12)))
        assert len({m['sequence'] for m in sent}) == 12
        assert len({m['id'] for m in sent}) == 12
        assert len(room('bob', 'messages', did)['messages']) == 16
        passed('Twelve concurrent CLI sends by two users: unique sequence and no lost messages')
        report['room_ids'] = {'direct': did, 'group': gid, 'channel': cid}

        # Revocation closes only the two freshly created test accounts' sessions.
        for actor in ('bob', 'eve'):
            sessions = call(actor, 'session', 'list')
            for session in rows(sessions, 'sessions'):
                call(actor, 'session', 'revoke', session['id'], '--yes', raw=True)
        call('owner', 'workspace', 'delete', workspace, '--confirm', ws['slug'], '--yes', raw=True)
        report['cleanup'] = True
        passed('Test login sessions revoked and exact QA workspace deleted')
        report['status'] = 'passed'
    except Exception as exc:
        report['status'] = 'failed'
        report['error'] = str(exc)
        report['private_state_directory'] = str(private)
        raise
    finally:
        Path(args.report).write_text(json.dumps(report, indent=2)+'\n')
        if report['cleanup']:
            for path in private.iterdir():
                path.unlink()
            private.rmdir()
        print('Report:', args.report, flush=True)


if __name__ == '__main__':
    main()
