#!/usr/bin/env python3
"""Opt-in Dev2 demo: real issue/routine events and one real agent status answer.

Requires prepare-chat-demo.py and the Dev2 owner CLI profile. Keeps labelled
demo resources for review. Never touches real issues or changes passwords.
"""
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
    workspace, channel = state['workspace_id'], state['channel_id']
    report = {'workspace_id': workspace, 'channel_id': channel, 'checks': []}
    base = [args.binary, '--server', 'http://localhost:8082', '--workspace', workspace, '-f', 'json', '--no-color']

    def cli(*words, actor='owner', expected=None, raw=False):
        env = dict(os.environ)
        command = base + ['--profile', 'dev2']
        if actor != 'owner':
            env = {k: v for k, v in env.items() if not k.startswith('CREWSHIP_')}
            env['CREWSHIP_CONFIG'] = f'/srv/crewship/.dev2-chat-demo/{actor}.yaml'
            command = base
        result = subprocess.run(command + list(words), env=env, text=True,
                                capture_output=True, timeout=150)
        if expected:
            assert result.returncode != 0
            failure = json.loads(result.stdout or result.stderr)
            assert failure['error']['status'] == expected, failure
            return failure
        assert result.returncode == 0, f'CLI failed: {words[:3]}: {result.stderr[:800]}'
        return result.stdout if raw else (json.loads(result.stdout) if result.stdout.strip() else None)

    def room(*words, **kw):
        return cli('chat', 'room', *words, **kw)

    def passed(name):
        report['checks'].append(name)
        print('PASS', name, flush=True)

    def wait(fn, timeout=20):
        end = time.monotonic()+timeout
        while time.monotonic() < end:
            result = fn()
            if result:
                return result
            time.sleep(.5)
        raise AssertionError('Timed out waiting for '+fn.__name__)

    def history():
        return room('messages', channel)['messages']

    try:
        assert room('get', channel)['title'] == 'Týmové plánování · demo'
        original_job_ids = {job['id'] for job in room('jobs', channel)['jobs']}
        room('activity', channel, '--issues=true', '--routines=true')
        assert room('activity', channel, actor='klara') == {'issues': True, 'routines': True}
        room('activity', channel, '--issues=false', '--routines=false', actor='klara', expected=404)
        passed('Channel creator enables activity; ordinary member reads but cannot reconfigure')
        title = 'Demo: ověřit plánování v týmovém chatu'
        found = cli('issue', 'list', '--crew', 'copy-site', '--search', title)
        found = found if isinstance(found, list) else found['issues']
        issue = next((item for item in found if item['title'] == title), None)
        if issue is None:
            cli('issue', 'create', '--crew', 'copy-site', '--title', title,
                '--description', 'Označené demo pro týmový Chat. Nejde o skutečný závazek týmu.', '--priority', 'low')
            # Legacy issue create reports success on stderr even in JSON mode;
            # verify persistence through its structured list/read commands.
            created = cli('issue', 'list', '--crew', 'copy-site', '--search', title)
            created = created if isinstance(created, list) else created['issues']
            issue = next(item for item in created if item['title'] == title)
        identifier = issue['identifier']
        report['issue_id'], report['issue_identifier'] = issue['id'], identifier
        current = cli('issue', 'get', identifier)
        if current['status'] == 'BACKLOG':
            cli('issue', 'update', identifier, '--status', 'TODO')
        assert cli('issue', 'get', identifier)['status'] == 'TODO', 'Existing demo issue was changed; do not overwrite it'
        cards = wait(lambda: [m for m in history() if m.get('source_kind') == 'activity'
                              and identifier in m['content'] and 'TODO' in m['content']])
        assert all(not m.get('author_user_id') and not m.get('author_agent_id') for m in cards)
        assert title in cards[0]['content'] and '/issues/' in cards[0]['content']
        passed('Real issue status update projects a named, linked, trusted activity card')

        routine_slug = 'chat-team-demo-check'
        routines = cli('routine', 'list')
        routines = routines if isinstance(routines, list) else routines.get('pipelines', routines.get('routines', []))
        if not any(r['slug'] == routine_slug for r in routines):
            cli('routine', 'save', '--name', 'Chat team demo check',
                '--definition', str(Path(__file__).with_name('chat-team-routine.json')),
                '--author-crew', 'copy-site', '--trigger-manual', raw=True)
        result = cli('routine', 'run', routine_slug, '--wait', '--wait-timeout', '60s',
                     '--idempotency-key', 'team-chat-demo-20260907-v2', raw=True)
        report['routine_result'] = result
        wait(lambda: [m for m in history() if m.get('source_kind') == 'activity'
                      and routine_slug in m['content'] and 'Routine run completed' in m['content']])
        passed('Real token-free transform routine completes and posts its linked channel result')
        initial_jobs = room('jobs', channel)['jobs']
        assert {job['id'] for job in initial_jobs} == original_job_ids, 'System activity created an agent job'
        passed('Issue/routine system activity does not launch agents')

        agents = cli('agent', 'list', '--q', 'ma-ena')
        agent = next(a for a in agents if a['slug'] == 'ma-ena')['id']
        room('agents', 'add', channel, agent)
        question = f'Jaký aktuální stav má issue {identifier}? Odpověz stručně česky, uveď identifikátor, přesný stav a odkaz. Použij přiložený přehled práce. Nic neměň a nepoužívej nástroje.'
        message = room('send', channel, '-m', question, '--client-id', 'team-demo-status-20260907', '--mention-agent', agent)
        def finished():
            jobs = [j for j in room('jobs', channel)['jobs'] if j['message_id'] == message['id']]
            if jobs and jobs[0]['state'] == 'failed':
                raise AssertionError('Agent job failed: '+jobs[0].get('error', ''))
            return jobs if jobs and jobs[0]['state'] == 'completed' else None
        jobs = wait(finished, timeout=120)
        replies = [m for m in history() if m.get('author_agent_id') == agent and identifier in m['content']]
        assert replies and 'TODO' in replies[-1]['content'] and '/issues/' in replies[-1]['content'], 'Agent reply does not match actual issue/link'
        assert cli('issue', 'get', identifier)['status'] == 'TODO'
        report['agent_job_id'] = jobs[0]['id']
        report['agent_reply_id'] = replies[-1]['id']
        passed('Real Mařena answers the exact stored issue status with a link and changes no work')
        before = len(history())
        room('send', channel, '-m', question, '--client-id', 'team-demo-status-20260907', '--mention-agent', agent)
        time.sleep(1.5)
        assert len(history()) == before
        assert len([j for j in room('jobs', channel)['jobs'] if j['message_id'] == message['id']]) == 1
        passed('Retried structured mention does not duplicate the job or reply')
        report['status'] = 'passed'
    except Exception as error:
        report['status'] = 'failed'
        report['error'] = str(error)
        raise
    finally:
        Path(args.report).write_text(json.dumps(report, ensure_ascii=False, indent=2)+'\n')
        print('Report:', args.report, flush=True)


if __name__ == '__main__':
    main()
