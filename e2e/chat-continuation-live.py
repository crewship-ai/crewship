#!/usr/bin/env python3
"""Opt-in Dev2: private DM → group → explicit agent channel, keeping labelled demo rooms."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='/tmp/crewship-2-dev')
    parser.add_argument('--report', required=True)
    args = parser.parse_args()
    state = json.loads(Path('/srv/crewship/.dev2-chat-demo/accounts.json').read_text())
    ws, owner = state['workspace_id'], state['owner_id']
    klara, tomas = state['accounts']['klara'], state['accounts']['tomas']
    source = klara['direct_conversation_id']
    report = {'workspace_id': ws, 'source_id': source, 'checks': []}

    def cli(*words, actor='owner', expected=None):
        env = dict(os.environ)
        base = [args.binary, '--server', 'http://localhost:8082', '--workspace', ws, '-f', 'json', '--no-color']
        if actor == 'owner':
            base += ['--profile', 'dev2']
        else:
            env = {k: v for k, v in env.items() if not k.startswith('CREWSHIP_')}
            env['CREWSHIP_CONFIG'] = f'/srv/crewship/.dev2-chat-demo/{actor}.yaml'
        result = subprocess.run(base+list(words), capture_output=True, text=True, env=env, timeout=150)
        if expected:
            assert result.returncode != 0
            assert json.loads(result.stdout or result.stderr)['error']['status'] == expected
            return None
        assert result.returncode == 0, result.stderr[:800]
        return json.loads(result.stdout)

    def room(*words, **kw):
        return cli('chat', 'room', *words, **kw)

    def history(id, **kw):
        return room('messages', id, **kw)['messages']

    def passed(name):
        report['checks'].append(name)
        print('PASS', name, flush=True)

    try:
        before = history(source)
        assert room('get', source)['is_direct']
        group_args = ('continue', source, '--title', 'Plánování · Klára a Tomáš · demo', '--member', tomas['user_id'], '--client-id', 'team-continuation-group-20260907')
        group = room(*group_args)
        gid = report['group_id'] = group['id']
        assert not group['is_direct'] and group['access_scope'] == 'participants'
        assert room(*group_args)['id'] == gid
        assert {p['user_id'] for p in room('participants', 'list', gid)['participants']} == {owner, klara['user_id'], tomas['user_id']}
        assert not ({m['id'] for m in before} & {m['id'] for m in history(gid)})
        room('get', source, actor='tomas', expected=404)
        passed('Private direct continues into one retry-safe group; original private access is unchanged')
        for actor, message in [('owner', 'Demo: plánování v nové soukromé skupině.'), ('klara', 'Demo: Klára je ve skupině a může odpovědět.'), ('tomas', 'Demo: Tomáš vidí novou skupinu, ne původní soukromé zprávy.')]:
            room('send', gid, '-m', message, '--client-id', f'continuation-demo-{actor}', actor=actor)
        assert {m.get('author_user_id') for m in history(gid)} >= {owner, klara['user_id'], tomas['user_id']}
        assert history(source) == before
        assert room('direct', klara['user_id'])['id'] == source
        passed('Three independently authenticated people exchange messages; source DM history stays unchanged')
        agent = next(a for a in cli('agent', 'list', '--q', 'ma-ena') if a['slug'] == 'ma-ena')['id']
        channel_args = ('continue', gid, '--kind', 'channel', '--title', 'Plánování s Mařenou · demo', '--agent', agent, '--client-id', 'team-continuation-channel-20260907')
        channel = room(*channel_args)
        cid = report['channel_id'] = channel['id']
        assert channel['access_scope'] == 'workspace'
        assert room(*channel_args)['id'] == cid
        assert [a['agent_id'] for a in room('agents', 'list', cid)['agents']] == [agent]
        assert not ({m['content'] for m in history(gid)} & {m['content'] for m in history(cid)})
        previous_jobs = room('jobs', cid)['jobs']
        assert not previous_jobs or all(j['state'] == 'completed' for j in previous_jobs)
        passed('Explicit workspace channel atomically includes the agent without private history or an automatic run')
        issue = cli('issue', 'get', 'COP-1')
        message = room('send', cid, '-m', 'Jaký stav má issue COP-1? Odpověz stručně česky, uveď přesný stav a odkaz podle přehledu práce. Nic neměň a nepoužívej nástroje.', '--mention-agent', agent, '--client-id', 'continuation-demo-agent-status', actor='klara')
        end = time.monotonic()+120
        while time.monotonic() < end:
            jobs = [j for j in room('jobs', cid)['jobs'] if j['message_id'] == message['id']]
            if jobs and jobs[0]['state'] == 'failed':
                raise AssertionError('Agent job failed: '+jobs[0].get('error', ''))
            if jobs and jobs[0]['state'] == 'completed':
                break
            time.sleep(.5)
        else:
            raise AssertionError('Timed out waiting for mentioned agent')
        replies = [m for m in history(cid) if m.get('author_agent_id') == agent]
        assert replies and 'COP-1' in replies[-1]['content'] and issue['status'] in replies[-1]['content'] and '/issues/' in replies[-1]['content']
        assert cli('issue', 'get', 'COP-1')['status'] == issue['status']
        report['agent_job_id'], report['reply_id'] = jobs[0]['id'], replies[-1]['id']
        passed('A colleague mentions the invited agent; its real reply matches the stored issue status and link')
        report['status'] = 'passed'
    except Exception as error:
        report.update(status='failed', error=str(error))
        raise
    finally:
        Path(args.report).write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')


if __name__ == '__main__':
    main()
