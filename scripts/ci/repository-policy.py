#!/usr/bin/env python3
"""Plan a preservation-first main ruleset update; writes require an explicit saved plan."""
import argparse
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import re
import subprocess

FIELDS = ('name', 'target', 'enforcement', 'conditions', 'bypass_actors', 'rules')
CHECKS = {'CI Result', 'Security Result', 'CodeQL Result'}
QUEUE = {'check_response_timeout_minutes': 120, 'grouping_strategy': 'ALLGREEN',
         'max_entries_to_build': 1, 'max_entries_to_merge': 1, 'merge_method': 'SQUASH',
         'min_entries_to_merge': 1, 'min_entries_to_merge_wait_minutes': 0}
SECURITY = ('none', 'critical', 'high_or_higher', 'medium_or_higher', 'all')
ALERTS = ('none', 'errors', 'errors_and_warnings', 'all')


def shape(ruleset):
    if any(field not in ruleset for field in FIELDS):
        raise ValueError('ruleset is missing a writable policy field')
    return deepcopy({field: ruleset[field] for field in FIELDS})


def digest(policy):
    return hashlib.sha256(json.dumps(policy, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def validate(policy):
    if policy['target'] != 'branch' or policy['enforcement'] != 'active':
        raise ValueError('expected an active branch ruleset')
    if policy['conditions'] != {'ref_name': {'include': ['refs/heads/main'], 'exclude': []}}:
        raise ValueError('expected main-only conditions without exclusions')
    rules = policy['rules']
    types = [r['type'] for r in rules]
    if len(types) != len(set(types)):
        raise ValueError('duplicate rule types require manual review')
    by_type = {r['type']: r for r in rules}
    checks = by_type.get('required_status_checks', {}).get('parameters', {})
    if checks.get('strict_required_status_checks_policy') is not True:
        raise ValueError('strict required checks must remain enabled')
    configured = checks.get('required_status_checks', [])
    for name in CHECKS:
        matches = [c for c in configured if c.get('context') == name]
        if len(matches) != 1 or matches[0].get('integration_id') != 15368:
            raise ValueError(f'missing or unexpected GitHub Actions required check: {name}')
    review = by_type.get('pull_request', {}).get('parameters', {})
    if review.get('required_approving_review_count', 0) < 1:
        raise ValueError('an approving review is required')
    for flag in ('dismiss_stale_reviews_on_push', 'require_last_push_approval',
                 'required_review_thread_resolution'):
        if review.get(flag) is not True:
            raise ValueError(f'review protection missing: {flag}')
    if 'squash' not in review.get('allowed_merge_methods', []):
        raise ValueError('existing pull-request policy does not allow squash')
    return by_type


def proposed(before, code_scanning_only=False):
    result = shape(before)
    by_type = validate(result)
    if code_scanning_only:
        pass  # Independent security policy leaves any existing queue untouched.
    elif 'merge_queue' in by_type:
        parameters = by_type['merge_queue']['parameters']
        # Preserve future fields instead of replacing the whole parameters object.
        parameters.update(QUEUE)
    else:
        result['rules'].append({'type': 'merge_queue', 'parameters': deepcopy(QUEUE)})
    if 'code_scanning' not in by_type:
        result['rules'].append({'type': 'code_scanning', 'parameters': {'code_scanning_tools': []}})
    tools = next(r for r in result['rules'] if r['type'] == 'code_scanning')['parameters']['code_scanning_tools']
    matches = [tool for tool in tools if tool.get('tool') == 'CodeQL']
    if len(matches) > 1:
        raise ValueError('duplicate CodeQL policies require manual review')
    if not matches:
        tools.append({'tool': 'CodeQL', 'alerts_threshold': 'none',
                      'security_alerts_threshold': 'high_or_higher'})
    else:
        tool = matches[0]
        if tool.get('alerts_threshold') not in ALERTS or tool.get('security_alerts_threshold') not in SECURITY:
            raise ValueError('unknown CodeQL threshold; refusing to weaken policy')
        # none disables ordinary-quality alerts. Existing blocking settings stay intact.
        current = tool['security_alerts_threshold']
        if SECURITY.index(current) < SECURITY.index('high_or_higher'):
            tool['security_alerts_threshold'] = 'high_or_higher'
    return result


def identity(ruleset, repo, ruleset_id):
    if (ruleset.get('id') != ruleset_id or ruleset.get('source_type') != 'Repository'
            or ruleset.get('source') != repo):
        raise ValueError('ruleset identity/source does not match the requested repository')


def gh_api(endpoint, payload=None):
    command = ['gh', 'api', endpoint, '-H', 'X-GitHub-Api-Version: 2026-03-10']
    if payload is not None:
        command += ['--method', 'PUT', '--input', '-']
    return json.loads(subprocess.check_output(command, input=None if payload is None else
                                            json.dumps(payload).encode(), timeout=90))


def make_plan(ruleset, repo, ruleset_id, auto_merge, code_scanning_only=False):
    identity(ruleset, repo, ruleset_id)
    before = shape(ruleset)
    warnings = []
    if before['bypass_actors']:
        warnings.append('Preserved emergency bypass actors can override required checks and '
                        'merge queue; review their use separately.')
    if auto_merge is False and not code_scanning_only:
        warnings.append('Repository allow_auto_merge is false; an operator must enable it '
                        'before activating the queue procedure. This plan does not change it.')
    return {'schema_version': 1, 'repo': repo, 'ruleset_id': ruleset_id,
            'expected_sha256': digest(before), 'before': before,
            'code_scanning_only': code_scanning_only,
            'proposed': proposed(before, code_scanning_only),
            'allow_auto_merge_observed': auto_merge, 'warnings': warnings}


def main_ancestor(repo, commit, label, api):
    if not re.fullmatch(r'[0-9a-f]{40}', commit or ''):
        raise ValueError(f'{label} prerequisite requires a full commit SHA')
    comparison = api(f'repos/{repo}/compare/{commit}...main')
    if (not isinstance(comparison, dict) or not isinstance(comparison.get('base_commit'), dict)
            or comparison['base_commit'].get('sha') != commit):
        raise ValueError(f'{label} comparison does not match the reviewed commit SHA')
    if comparison.get('status') not in ('ahead', 'identical'):
        raise ValueError(f'{label} commit is not an ancestor of current main')


def apply_plan(plan, documentation_commit, analysis_commit, api=gh_api):
    repo, ruleset_id = plan['repo'], plan['ruleset_id']
    if plan.get('schema_version') != 1 or digest(plan['before']) != plan['expected_sha256']:
        raise ValueError('invalid saved plan fingerprint')
    if not isinstance(plan.get('code_scanning_only', False), bool):
        raise ValueError('invalid policy scope')
    security_only = plan.get('code_scanning_only', False)
    if proposed(plan['before'], security_only) != plan['proposed']:
        raise ValueError('saved proposed policy differs from the preservation-first transformation')
    # SHAs attest operator review of the source/canaries and queue procedure.
    # Ancestry verifies presence on main, not those commits' actual content.
    if security_only and documentation_commit is not None:
        raise ValueError('documentation commit is only valid for queue activation')
    if not re.fullmatch(r'[0-9a-f]{40}', analysis_commit or ''):
        raise ValueError('analysis prerequisite requires a full commit SHA')
    if not security_only and not re.fullmatch(r'[0-9a-f]{40}', documentation_commit or ''):
        raise ValueError('documentation prerequisite requires a full commit SHA')
    main_ancestor(repo, analysis_commit, 'analysis', api)
    if not security_only:
        main_ancestor(repo, documentation_commit, 'documentation', api)
    endpoint = f'repos/{repo}/rulesets/{ruleset_id}'
    current = api(endpoint)
    identity(current, repo, ruleset_id)
    validate(shape(current))
    if digest(shape(current)) != plan['expected_sha256']:
        raise ValueError('live policy changed since planning; regenerate and review the plan')
    if shape(current) == plan['proposed']:
        return {'changed': False, 'sha256': digest(shape(current))}
    updated = api(endpoint, plan['proposed'])
    try:
        if not isinstance(updated, dict):
            raise ValueError('update returned a malformed policy')
        identity(updated, repo, ruleset_id)
        if shape(updated) != plan['proposed']:
            raise ValueError('update returned an unexpected policy')
    except ValueError as error:
        raise ValueError('update response differs from the reviewed policy; live policy may '
                         'have changed; inspect live ruleset before continuing') from error
    return {'changed': True, 'sha256': digest(shape(updated))}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', default='crewship-ai/crewship')
    parser.add_argument('--ruleset-id', type=int, default=16547292)
    parser.add_argument('--output', type=Path, help='save dry-run plan (otherwise print it)')
    parser.add_argument('--code-scanning-only', action='store_true',
                        help='plan independent CodeQL policy without changing merge queue')
    parser.add_argument('--apply', action='store_true', help='explicitly apply the reviewed saved plan')
    parser.add_argument('--plan', type=Path, help='saved plan required for --apply')
    parser.add_argument('--documentation-commit', help='full SHA of reviewed queue documentation already on main')
    parser.add_argument('--analysis-commit', help='full SHA of reviewed PR analysis workflow already on main')
    args = parser.parse_args()
    if not re.fullmatch(r'[\w.-]+/[\w.-]+', args.repo) or args.ruleset_id < 1:
        parser.error('expected owner/repo and positive ruleset id')
    if args.apply:
        if not args.plan or args.code_scanning_only:
            parser.error('--apply requires --plan; scope is read from the saved plan')
        plan = json.loads(args.plan.read_text())
        if not re.fullmatch(r'[0-9a-f]{40}', args.analysis_commit or ''):
            parser.error('all policy activation requires a full --analysis-commit SHA')
        if plan.get('code_scanning_only', False) and args.documentation_commit is not None:
            parser.error('--documentation-commit is only valid for queue activation')
        if not plan.get('code_scanning_only', False) and not re.fullmatch(r'[0-9a-f]{40}', args.documentation_commit or ''):
            parser.error('queue activation requires a full --documentation-commit SHA')
        if plan.get('repo') != args.repo or plan.get('ruleset_id') != args.ruleset_id:
            parser.error('saved plan target differs from command target')
        print(json.dumps(apply_plan(plan, args.documentation_commit, args.analysis_commit), indent=2))
        return
    if args.plan or args.documentation_commit or args.analysis_commit:
        parser.error('--plan/--documentation-commit/--analysis-commit only accompany --apply')
    ruleset = gh_api(f'repos/{args.repo}/rulesets/{args.ruleset_id}')
    repo = gh_api(f'repos/{args.repo}')
    if repo.get('default_branch') != 'main':
        raise ValueError('repository default branch is not main')
    plan = make_plan(ruleset, args.repo, args.ruleset_id, repo.get('allow_auto_merge'), args.code_scanning_only)
    rendered = json.dumps(plan, indent=2) + '\n'
    if args.output:
        args.output.write_text(rendered)
        print(f'Dry-run plan saved to {args.output}; no settings changed.')
    else:
        print(rendered, end='')


if __name__ == '__main__':
    main()
