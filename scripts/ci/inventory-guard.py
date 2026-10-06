#!/usr/bin/env python3
"""Trusted-main CI control review. PR contents are bounded data, never executed."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
MANIFEST = 'scripts/ci/required-inventory.json'
PROTECTED = ('.github/workflows/ci.yml', '.github/workflows/security.yml', '.github/workflows/codeql.yml',
             'scripts/ci/plan.py', 'scripts/ci/verdict.py')
WORKFLOWS = dict(zip(PROTECTED[:3], ('CI Result', 'Security Result', 'CodeQL Result')))
SELF = ('scripts/ci/inventory-guard.py', '.github/workflows/ci-inventory.yml', MANIFEST)
CONTEXT = 'CI Inventory Guard'
LIMIT = 256 * 1024
TREE_LIMIT = 5 * 1024 * 1024
REPOSITORY = 'crewship-ai/crewship'
SHA = re.compile(r'[0-9a-f]{40}')


class Rejected(ValueError):
    pass


def text(raw):
    if len(raw) > LIMIT or b'\0' in raw:
        raise Rejected('control file exceeds limit or contains binary data')
    try:
        return raw.decode('utf-8')
    except UnicodeDecodeError:
        raise Rejected('control file is not UTF-8 text') from None


def read_control(root, path):
    file = root / path
    if any((root / Path(*Path(path).parts[:index])).is_symlink()
           for index in range(1, len(Path(path).parts) + 1)):
        raise Rejected('control path must not contain symlinks')
    if not file.is_file() or file.stat().st_size > LIMIT:
        raise Rejected('control file missing or exceeds limit: ' + path)
    with file.open('rb') as stream:
        raw = stream.read(LIMIT + 1)
    text(raw)
    return raw


def inventory(raw, expected_name):
    source = text(raw)
    blocks = re.findall(r'^  ([a-z][a-z0-9_-]*):\n(.*?)(?=^  [a-z][a-z0-9_-]*:\n|\Z)', source.split('jobs:\n', 1)[-1], re.M | re.S)
    jobs = [name for name, _ in blocks]
    if not jobs or len(jobs) != len(set(jobs)) or jobs.count('result') != 1:
        raise Rejected('missing or duplicate workflow jobs/result')
    result = dict(blocks)['result']
    if not re.search(r'^    name: ' + re.escape(expected_name) + r'\s*$', result, re.M):
        raise Rejected('aggregate check name changed or missing')
    match = re.search(r'^    needs: \[([^\]\n]+)\]\s*$', result, re.M)
    if not match:
        raise Rejected('aggregate needs must be an explicit one-line list')
    required = [value.strip() for value in match[1].split(',')]
    if len(required) != len(set(required)) or set(required) - set(jobs):
        raise Rejected('aggregate needs contains missing or duplicate jobs')
    return {'jobs': sorted(jobs), 'required_jobs': sorted(required)}


def manifest_data(raw):
    data = json.loads(text(raw))
    if (not isinstance(data, dict) or type(data.get('version')) is not int or data['version'] != 1
            or not isinstance(data.get('protected_files'), dict) or set(data['protected_files']) != set(PROTECTED)
            or not isinstance(data.get('workflows'), dict) or set(data['workflows']) != set(WORKFLOWS)):
        raise Rejected('inventory schema or protected path set is invalid')
    for digest in data['protected_files'].values():
        if not isinstance(digest, str) or not re.fullmatch(r'[0-9a-f]{64}', digest):
            raise Rejected('inventory control hash is invalid')
    for path, entry in data['workflows'].items():
        if not isinstance(entry, dict) or entry.get('context') != WORKFLOWS[path]:
            raise Rejected('inventory aggregate context is invalid')
        jobs = entry.get('required_jobs')
        if (not isinstance(jobs, list) or not jobs or len(jobs) > 100
                or any(not isinstance(job, str) or not re.fullmatch(r'[a-z][a-z0-9_-]*', job) for job in jobs)
                or jobs != sorted(set(jobs))):
            raise Rejected('inventory required job list is invalid')
    return data


def refreshed(snapshot):
    """Read candidate control files as data; never import or run candidate helpers."""
    hashes, workflows = {}, {}
    for path in PROTECTED:
        raw = snapshot[path]
        text(raw)
        hashes[path] = hashlib.sha256(raw).hexdigest()
        if path in WORKFLOWS:
            workflows[path] = {'context': WORKFLOWS[path], 'required_jobs': inventory(raw, WORKFLOWS[path])['required_jobs']}
    return {'version': 1, 'protected_files': hashes, 'workflows': workflows}


def candidate_manifest_errors(raw, snapshot):
    try:
        manifest = manifest_data(raw)
        current = refreshed(snapshot)
        return ['candidate inventory is stale; regenerate its protected hashes and job dependencies'] if manifest != current else []
    except (Rejected, ValueError, KeyError, TypeError):
        return ['candidate inventory is malformed or disagrees with candidate controls']


class API:
    def __init__(self, token):
        self.token = token
        self.calls = 0

    def __call__(self, endpoint, payload=None):
        self.calls += 1
        if self.calls > 32 or not endpoint.startswith('repos/'):
            raise Rejected('API request budget or endpoint rejected')
        body = json.dumps(payload).encode() if payload is not None else None
        request = urllib.request.Request('https://api.github.com/' + endpoint, data=body,
            headers={'Authorization': 'Bearer ' + self.token, 'Accept': 'application/vnd.github+json',
                     'X-GitHub-Api-Version': '2022-11-28', 'Content-Type': 'application/json'},
            method='POST' if payload is not None else 'GET')
        # Never follow a redirect with a privileged credential.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args):
                return None
        try:
            with urllib.request.build_opener(NoRedirect).open(request, timeout=20) as response:
                response_limit = TREE_LIMIT if '/git/trees/' in endpoint else LIMIT * 2
                raw = response.read(response_limit + 1)
                if len(raw) > response_limit:
                    raise Rejected('API response exceeds limit')
                return json.loads(raw)
        except (urllib.error.URLError, ValueError) as error:
            raise Rejected('GitHub API request failed or returned invalid data') from None


def candidate_tree(api, repo, sha, paths):
    # Contents can dereference symlinks: require original Git modes separately.
    data = api('repos/' + repo + '/git/trees/' + sha + '?recursive=1')
    if (not isinstance(data, dict) or data.get('truncated') is not False
            or not isinstance(data.get('tree'), list) or len(data['tree']) > 30000):
        raise Rejected('candidate Git tree missing, truncated or exceeds entry limit')
    entries = {}
    for entry in data['tree']:
        if not isinstance(entry, dict):
            raise Rejected('invalid candidate Git tree entry')
        path = entry.get('path')
        if not isinstance(path, str):
            raise Rejected('invalid candidate Git tree path')
        if path not in paths:
            continue
        if (path in entries or entry.get('type') != 'blob'
                or entry.get('mode') not in ('100644', '100755')
                or not isinstance(entry.get('sha'), str) or not SHA.fullmatch(entry['sha'])
                or type(entry.get('size')) is not int or not 0 <= entry['size'] <= LIMIT):
            raise Rejected('candidate control must be a unique bounded regular Git blob: ' + path)
        entries[path] = entry
    if set(entries) != set(paths):
        raise Rejected('candidate Git tree is missing protected control paths')
    return entries


def content(api, repo, path, sha, entry):
    data = api('repos/' + repo + '/contents/' + urllib.parse.quote(path, safe='/') + '?ref=' + sha)
    if (not isinstance(data, dict) or data.get('type') != 'file' or data.get('path') != path
            or data.get('encoding') != 'base64' or type(data.get('size')) is not int
            or not 0 <= data['size'] <= LIMIT):
        raise Rejected('missing, oversized or unsupported control file: ' + path)
    try:
        raw = base64.b64decode(''.join(data['content'].split()), validate=True)
    except (ValueError, KeyError, TypeError):
        raise Rejected('invalid content encoding: ' + path) from None
    blob_sha = hashlib.sha1(b'blob ' + str(len(raw)).encode() + b'\0' + raw).hexdigest()
    if (len(raw) != data['size'] or len(raw) != entry['size']
            or data.get('sha') != entry['sha'] or blob_sha != entry['sha']):
        raise Rejected('control file size or Git blob hash mismatch: ' + path)
    text(raw)
    return raw


def admin(api, repo, actor):
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,38}', actor):
        raise Rejected('invalid dispatch actor')
    permissions = api('repos/' + repo + '/collaborators/' + actor + '/permission')
    if (permissions.get('permission') != 'admin' or
            permissions.get('user', {}).get('login', '').casefold() != actor.casefold()):
        raise Rejected('dispatch requires verified repository administrator permission')


def verify_pr(pr, repo, number, head):
    if (pr.get('state') != 'open' or pr.get('number') != number
            or pr.get('base', {}).get('ref') != 'main'
            or pr.get('base', {}).get('repo', {}).get('full_name') != repo
            or pr.get('head', {}).get('sha') != head):
        raise Rejected('PR repository, base, state or expected head changed')


def verify_queue(api, repo, ref, head, trusted):
    if (not isinstance(ref, str) or not re.fullmatch(r'refs/heads/gh-readonly-queue/main/[A-Za-z0-9_.-]{1,200}', ref)
            or '..' in ref):
        raise Rejected('queue target must be an explicit generated main queue branch')
    queue = api('repos/' + repo + '/git/ref/' + urllib.parse.quote(ref.removeprefix('refs/'), safe='/'))
    main = api('repos/' + repo + '/git/ref/heads/main')
    if (queue.get('ref') != ref or queue.get('object', {}).get('type') != 'commit'
            or queue['object'].get('sha') != head or main.get('ref') != 'refs/heads/main'
            or main.get('object', {}).get('type') != 'commit' or main['object'].get('sha') != trusted):
        raise Rejected('queue head or current trusted main base changed')


def inspect(event, env, api, root=ROOT):
    repo = env.get('GITHUB_REPOSITORY')
    if repo != REPOSITORY or event.get('repository', {}).get('full_name') != repo:
        raise Rejected('unexpected repository')
    trusted = env.get('GUARD_TRUSTED_SHA', env.get('GITHUB_SHA', ''))
    if event['repository'].get('default_branch') != 'main' or not SHA.fullmatch(trusted):
        raise Rejected('trusted default-branch baseline is unavailable')
    kind = env.get('GITHUB_EVENT_NAME')
    approved, number, queue_ref = False, None, None
    if kind == 'pull_request_target':
        number = event.get('number')
        head = event.get('pull_request', {}).get('head', {}).get('sha')
    elif kind == 'merge_group':
        group = event.get('merge_group', {})
        if group.get('base_ref') != 'refs/heads/main' or group.get('base_sha') != trusted:
            raise Rejected('merge group must use the trusted main base')
        head, queue_ref = group.get('head_sha'), group.get('head_ref')
    elif kind == 'workflow_dispatch':
        if env.get('GITHUB_REF') != 'refs/heads/main' or trusted != env.get('GITHUB_SHA'):
            raise Rejected('dispatch must run the trusted main workflow')
        inputs = event.get('inputs', {})
        head = inputs.get('expected_head_sha')
        queue_ref = inputs.get('queue_ref') or None
        raw_number = inputs.get('pr_number', '')
        if queue_ref:
            if raw_number or inputs.get('expected_base_sha') != trusted:
                raise Rejected('queue dispatch requires an exclusive queue target and exact main base')
        else:
            if not isinstance(raw_number, str) or not re.fullmatch(r'[1-9][0-9]{0,8}', raw_number):
                raise Rejected('dispatch requires an explicit PR number')
            number = int(raw_number)
        value = inputs.get('approve_control_changes')
        if type(value) not in (str, bool) or value not in ('true', 'false', True, False):
            raise Rejected('dispatch requires an explicit approval boolean')
        approved = value in ('true', True)
        for actor in set((env.get('GITHUB_ACTOR', ''), env.get('GITHUB_TRIGGERING_ACTOR', ''))):
            admin(api, repo, actor)
    else:
        raise Rejected('unsupported inventory guard event')
    if not isinstance(head, str) or not SHA.fullmatch(head):
        raise Rejected('missing or invalid candidate head identity')
    if queue_ref is not None:
        verify_queue(api, repo, queue_ref, head, trusted)
        head_repo = repo
    else:
        if type(number) is not int or number <= 0:
            raise Rejected('missing or invalid PR number')
        pr = api('repos/' + repo + '/pulls/' + str(number))
        verify_pr(pr, repo, number, head)
        head_repo = pr.get('head', {}).get('repo', {}).get('full_name', '')
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}', head_repo):
            raise Rejected('PR head repository unavailable')
    manifest = manifest_data(read_control(root, MANIFEST))
    baseline, candidate, changed, snapshot = {}, {}, [], {}
    protected = manifest['protected_files']
    paths = sorted(set(protected) | set(SELF))
    tree = candidate_tree(api, head_repo, head, paths)
    for path in paths:
        if path.startswith('/') or '..' in path.split('/'):
            raise Rejected('invalid trusted control path')
        before = read_control(root, path)
        text(before)
        if path in protected and hashlib.sha256(before).hexdigest() != protected[path]:
            raise Rejected('trusted baseline control hash mismatch: ' + path)
        after = content(api, head_repo, path, head, tree[path])
        snapshot[path] = after
        baseline[path] = {'sha256': hashlib.sha256(before).hexdigest()}
        candidate[path] = {'sha256': hashlib.sha256(after).hexdigest()}
        if before != after:
            changed.append(path)
        if path in manifest['workflows']:
            entry = manifest['workflows'][path]
            baseline[path].update(inventory(before, entry['context']))
            if baseline[path]['required_jobs'] != entry['required_jobs']:
                raise Rejected('trusted required job inventory disagrees with workflow')
            candidate[path].update(inventory(after, entry['context']))
            candidate[path]['removed_required_jobs'] = sorted(set(entry['required_jobs']) - set(candidate[path]['required_jobs']))
            candidate[path]['added_required_jobs'] = sorted(set(candidate[path]['required_jobs']) - set(entry['required_jobs']))
            candidate[path]['removed_jobs'] = sorted(set(baseline[path]['jobs']) - set(candidate[path]['jobs']))
            candidate[path]['added_jobs'] = sorted(set(candidate[path]['jobs']) - set(baseline[path]['jobs']))
    candidate_errors = candidate_manifest_errors(snapshot[MANIFEST], snapshot)
    return {'repository': repo, 'pr_number': number, 'head_sha': head,
            'queue_ref': queue_ref, 'trusted_sha': trusted, 'diff_url': 'https://github.com/' + repo + '/compare/' + trusted + '...' + head, 'event': kind, 'actor': env.get('GITHUB_ACTOR'),
            'triggering_actor': env.get('GITHUB_TRIGGERING_ACTOR'), 'approval_requested': approved,
            'changed_control_files': changed, 'baseline': baseline, 'candidate': candidate,
            'candidate_inventory_errors': candidate_errors,
            'state': 'success' if not candidate_errors and (not changed or approved) else 'failure',
            'decision': 'candidate inventory must be refreshed before approval' if candidate_errors else 'explicit administrator exception' if changed and approved else 'unchanged controls' if not changed else 'control changes require a separate administrator dispatch'}


def execute(event, env, api, root=ROOT):
    output = Path('.ci-results/ci-inventory-report.json')
    output.unlink(missing_ok=True)  # A failed local retry must not reuse old success evidence.
    report = inspect(event, env, api, root)
    # Recheck live candidate identity immediately before status publication.
    if report['queue_ref']:
        verify_queue(api, report['repository'], report['queue_ref'], report['head_sha'], report['trusted_sha'])
    else:
        latest = api('repos/' + report['repository'] + '/pulls/' + str(report['pr_number']))
        verify_pr(latest, report['repository'], report['pr_number'], report['head_sha'])
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + '\n')
    api('repos/' + report['repository'] + '/statuses/' + report['head_sha'], {
        'state': report['state'], 'context': CONTEXT,
        'description': report['decision'][:140],
        'target_url': 'https://github.com/' + report['repository'] + '/actions/runs/' + env['GITHUB_RUN_ID']})
    return report


if __name__ == '__main__':
    try:
        if len(sys.argv) == 3 and sys.argv[1] == '--refresh-inventory':
            candidate_root = Path(sys.argv[2]).resolve()
            snapshot = {path: read_control(candidate_root, path) for path in PROTECTED}
            destination = candidate_root / MANIFEST
            if destination.is_symlink():
                raise Rejected('inventory destination must not be a symlink')
            destination.write_text(json.dumps(refreshed(snapshot), indent=2) + '\n')
            print('Candidate inventory refreshed from five control files as data; no code executed')
            sys.exit(0)
        if len(sys.argv) != 1:
            raise Rejected('usage: inventory-guard.py [--refresh-inventory CANDIDATE_ROOT]')
        event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
        report = execute(event, os.environ, API(os.environ['GITHUB_TOKEN']))
        print(report['decision'])
        sys.exit(report['state'] != 'success')
    except (Rejected, OSError, ValueError, KeyError, TypeError) as error:
        # No raw response, token or PR-controlled text is printed.
        print('CI inventory guard failed closed: ' + str(error), file=sys.stderr)
        sys.exit(1)
