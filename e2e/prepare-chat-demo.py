#!/usr/bin/env python3
"""Prepare persistent, explicitly synthetic Chat colleagues on Dev2.

Uses the existing owner CLI profile and normal account setup/login. No database
writes, reset of claimed accounts, email delivery, agent runs or real tasks.
Passwords and isolated login configs remain in a private directory outside git.
Reruns reuse accounts, the channel and stable message IDs. The owner is unchanged.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='/tmp/crewship-2-dev')
    parser.add_argument('--server', default='http://localhost:8082')
    parser.add_argument('--profile', default='dev2')
    args = parser.parse_args()
    if args.server.rstrip('/') not in ('http://localhost:8082', 'http://127.0.0.1:8082'):
        parser.error('Persistent demo preparation is restricted to local Dev2.')
    os.umask(0o077)
    private = Path('/srv/crewship/.dev2-chat-demo')
    if private.is_symlink():
        raise RuntimeError('Private directory must not be a symlink')
    private.mkdir(mode=0o700, exist_ok=True)
    private.chmod(0o700)
    statefile = private / 'accounts.json'
    if statefile.is_symlink():
        raise RuntimeError('Private state must not be a symlink')
    state = json.loads(statefile.read_text()) if statefile.exists() else {'accounts': {}}
    workspace = ''

    def save():
        temporary = private / 'accounts.json.tmp'
        if temporary.is_symlink():
            raise RuntimeError('Private temporary state must not be a symlink')
        temporary.write_text(json.dumps(state, ensure_ascii=False, indent=2))
        temporary.chmod(0o600)
        temporary.replace(statefile)

    def call(actor, *words, scoped=True, stdin=None, raw=False):
        env = {k: v for k, v in os.environ.items() if not k.startswith('CREWSHIP_')}
        command = [args.binary, '--server', args.server, '--no-color', '-f', 'json']
        if actor == 'owner':
            command += ['--profile', args.profile]
        else:
            config = private / (actor + '.yaml')
            if config.is_symlink():
                raise RuntimeError('Private login config must not be a symlink')
            env['CREWSHIP_CONFIG'] = str(config)
        if scoped and workspace:
            command += ['--workspace', workspace]
        result = subprocess.run(command + list(words), env=env, input=stdin,
                                capture_output=True, text=True, timeout=45)
        # Subprocess output may contain credentials; never include it in errors.
        if result.returncode:
            raise RuntimeError(f'CLI failed for {actor}: {words[:3]}; secrets suppressed')
        if actor != 'owner' and config.exists():
            config.chmod(0o600)
        return result.stdout if raw or not result.stdout.strip() else json.loads(result.stdout)

    identity = call('owner', 'whoami', scoped=False)
    assert identity['server'].rstrip('/') == args.server.rstrip('/')
    assert identity['workspace']['role'] == 'OWNER'
    workspace = identity['workspace']['id']
    if state.get('workspace_id') not in (None, workspace):
        raise RuntimeError('Private demo state belongs to another workspace')
    state['workspace_id'] = workspace
    members = call('owner', 'workspace', 'member', 'list')
    owner = next(m for m in members if m['email'] == identity['user_email'])['user_id']
    state['owner_id'] = owner
    save()
    for actor, email, name in (
        ('klara', 'klara.chat-demo@crewship.invalid', 'Klára Nováková · demo'),
        ('tomas', 'tomas.chat-demo@crewship.invalid', 'Tomáš Dvořák · demo'),
    ):
        existing = next((m for m in members if m['email'] == email), None)
        account = state['accounts'].get(actor)
        if existing and not account:
            raise RuntimeError(f'{actor}: existing account has no locally owned credentials; refusing reprovision')
        if not account:
            account = {'email': email, 'full_name': name, 'password': secrets.token_urlsafe(32)}
            state['accounts'][actor] = account
            save()
        if account['email'] != email:
            raise RuntimeError('Unexpected stored account identity')
        if not existing:
            if account.get('setup_complete'):
                raise RuntimeError('Previously set up demo is no longer a member; refusing automatic reprovision')
            invitation = call('owner', 'workspace', 'member', 'invite', email)
            if not invitation.get('setup_url'):
                raise RuntimeError('Account is already claimed; refusing credential changes')
            account['setup_url'] = invitation['setup_url']
            save()
        if not account.get('setup_complete'):
            if not account.get('setup_url'):
                raise RuntimeError('Incomplete setup without owned token; inspect protected state')
            token = urllib.parse.parse_qs(urllib.parse.urlsplit(account['setup_url']).query)['token'][0]
            request = urllib.request.Request(args.server + '/api/v1/auth/reset',
                data=json.dumps({'token': token, 'new_password': account['password']}).encode(),
                headers={'Content-Type': 'application/json'}, method='POST')
            try:
                with urllib.request.urlopen(request, timeout=30) as response:
                    assert response.status == 200
            except Exception:
                raise RuntimeError('Account setup failed; response and token suppressed') from None
            account['setup_complete'] = True
            account.pop('setup_url', None)
            save()
        call(actor, 'login', '--email', email, '--password-stdin',
             stdin=account['password'] + '\n', scoped=False, raw=True)
        profile = call(actor, 'auth', 'profile', '--full-name', name)
        account['user_id'] = profile['id']
        save()

    rooms, offset = [], 0
    while True:
        page = call('owner', 'chat', 'room', 'list', '--offset', str(offset))
        rooms.extend(page['conversations'])
        if page.get('next_offset') is None:
            break
        offset = page['next_offset']
    channel = next((r for r in rooms if r['kind'] == 'channel' and r['title'] == 'Týmové plánování · demo'), None)
    if channel is None:
        channel = call('owner', 'chat', 'room', 'create', '--kind', 'channel', '--title', 'Týmové plánování · demo')
    state['channel_id'] = channel['id']
    for actor in ('klara', 'tomas'):
        dm = call(actor, 'chat', 'room', 'direct', owner)
        state['accounts'][actor]['direct_conversation_id'] = dm['id']
        greeting = ('Ahoj! Jsem ' + state['accounts'][actor]['full_name'] +
                    '. Toto je výslovně syntetická ukázka soukromé zprávy pro ověření Chatu; nejde o skutečného kolegu ani pracovní požadavek.')
        call(actor, 'chat', 'room', 'send', dm['id'], '-m', greeting, '--client-id', 'persistent-chat-demo-greeting-v1')
    for actor, message in (
        ('owner', 'DEMO: Tento kanál obsahuje pouze smyšlenou ukázku týmové komunikace. Klára a Tomáš jsou testovací účty. Zprávy nevytvářejí skutečné úkoly ani závazky.'),
        ('klara', 'Smyšlený příklad plánování: pro ukázkový projekt bych navrhla nejdřív projít návrh obrazovky. Žádné skutečné issue tím nevzniká.'),
        ('tomas', 'V rámci této smyšlené ukázky bych pak ověřil přístupnost a mobilní zobrazení. Tohle je demo výměna zpráv mezi dvěma samostatnými účty.'),
    ):
        call(actor, 'chat', 'room', 'send', channel['id'], '-m', message,
             '--client-id', 'persistent-chat-demo-planning-v1')
    # Verify persisted authors and retry deduplication, allowing later real demo use.
    history = call('owner', 'chat', 'room', 'messages', channel['id'], '--after-sequence', '0')['messages']
    seeded = [m for m in history if m['client_id'] == 'persistent-chat-demo-planning-v1']
    assert len(seeded) == 3
    assert {m['author_user_id'] for m in seeded} == {owner, *(a['user_id'] for a in state['accounts'].values())}
    for actor, account in state['accounts'].items():
        history = call('owner', 'chat', 'room', 'messages', account['direct_conversation_id'], '--after-sequence', '0')['messages']
        seeded = [m for m in history if m['client_id'] == 'persistent-chat-demo-greeting-v1']
        assert len(seeded) == 1 and seeded[0]['author_user_id'] == account['user_id']
    save()
    print(json.dumps({'workspace_id': workspace, 'owner_id': owner, 'channel_id': channel['id'],
        'accounts': {k: {field: v[field] for field in ('email', 'full_name', 'user_id', 'direct_conversation_id')}
                     for k, v in state['accounts'].items()},
        'protected_credentials': str(statefile)}, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
