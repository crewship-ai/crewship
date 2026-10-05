#!/usr/bin/env python3
"""Read-only, bounded GitHub PR/Actions throughput evidence; see pr-throughput.md."""
import argparse
from datetime import datetime, timezone
import json
import math
from pathlib import Path
import re
import subprocess

WORKFLOWS = {'ci.yml', 'security.yml', 'codeql.yml'}


def timestamp(value):
    return datetime.fromisoformat(value.replace('Z', '+00:00')) if value else None


def seconds(start, end):
    return (timestamp(end) - timestamp(start)).total_seconds() if start and end else None


def percentile(values, fraction):
    values = sorted(v for v in values if v is not None)
    return values[max(0, math.ceil(len(values) * fraction) - 1)] if values else None


def substantive(commit):
    # Main synchronization is proven by parents, never author or commit headline.
    return commit.get('main_parent_provenance') != 'verified_main_parent'


def annotate_main_parents(pr, repo, github, warnings, cache):
    commits = [n['commit'] for n in pr['commits']['nodes']]
    known = {c['oid'] for c in commits}
    merge = pr.get('mergeCommit') or {}
    base_parents = merge.get('parents', {}).get('nodes', [])
    base = base_parents[0]['oid'] if base_parents else None
    # A retained/rebased PR commit as the base parent is not independent main evidence.
    if base in known:
        base = None
    pr['main_provenance_base_sha'] = base
    for commit in commits:
        parents = commit['parents']
        if parents['totalCount'] < 2:
            commit['main_parent_provenance'] = 'single_parent'
            continue
        nodes = parents.get('nodes', [])
        if not base or len(nodes) != parents['totalCount']:
            commit['main_parent_provenance'] = 'unknown: missing independent main base or complete parents'
            warnings.append(f"PR {pr['number']} commit {commit['oid']}: main parent provenance unavailable")
            continue
        statuses = []
        for parent in nodes:
            pair = (parent['oid'], base)
            if pair not in cache:
                comparison = github.request(f'repos/{repo}/compare/{parent["oid"]}...{base}')
                cache[pair] = comparison.get('status')
            statuses.append(cache[pair])
        if any(status in ('ahead', 'identical') for status in statuses):
            commit['main_parent_provenance'] = 'verified_main_parent'
        elif all(status in ('behind', 'diverged') for status in statuses):
            commit['main_parent_provenance'] = 'verified_no_main_parent'
        else:
            commit['main_parent_provenance'] = 'unknown: unexpected ancestry comparison'
            warnings.append(f"PR {pr['number']} commit {commit['oid']}: main ancestry comparison unknown")


def push_evidence(pr):
    if any(n['commit']['parents']['totalCount'] >= 2 and
           n['commit'].get('main_parent_provenance', 'unknown').startswith('unknown')
           for n in pr['commits']['nodes']):
        return None, 'unavailable: unknown main-parent provenance'
    commits = [n['commit'] for n in pr['commits']['nodes'] if substantive(n['commit'])]
    if pr['commits']['pageInfo']['hasNextPage'] or pr['timelineItems']['pageInfo']['hasNextPage']:
        return None, 'unavailable: commit/timeline pagination cap reached'
    if not commits:
        return None, 'unavailable: no substantive commit'
    last = commits[-1]
    candidates = [(last.get('pushedDate'), 'GraphQL commit.pushedDate')]
    candidates += [(e['createdAt'], 'HeadRefForcePushedEvent') for e in pr['timelineItems']['nodes']
                   if (e.get('afterCommit') or {}).get('oid') == last['oid']]
    dates = [(date, source) for date, source in candidates if date and
             timestamp(date) <= timestamp(pr['mergedAt'])]
    if not dates:
        return None, 'unavailable: GitHub supplied no push timestamp for final substantive commit'
    # A force push may repush a substantive head. Report the latest observed push.
    return max(dates, key=lambda item: timestamp(item[0]))


def attempt_metrics(run, jobs):
    starts = [j['started_at'] for j in jobs if j.get('started_at') and j.get('steps')]
    ends = [j['completed_at'] for j in jobs if j.get('completed_at') and j.get('steps')]
    first, last = min(starts, default=None), max(ends, default=None)
    return {'run_id': run['id'], 'attempt': run['run_attempt'], 'workflow': run['path'],
            'head_sha': run['head_sha'], 'event': run['event'], 'conclusion': run['conclusion'],
            'created_at': run['created_at'],
            # Reruns retain the original created_at; their enqueue time is unavailable.
            'queue_seconds': seconds(run['created_at'], first) if run['run_attempt'] == 1 else None,
            'execution_wall_seconds': seconds(first, last),
            'elapsed_seconds': seconds(run['created_at'], last) if run['run_attempt'] == 1 else None,
            'job_execution_seconds': sum(seconds(j.get('started_at'), j.get('completed_at')) or 0
                                         for j in jobs if j.get('steps')),
            'timed_out_jobs': sum(j.get('conclusion') == 'timed_out' for j in jobs),
            'job_conclusions': {j['name']: j['conclusion'] for j in jobs}}


def row_run_counts(attempts):
    unique = {(a['run_id'], a['attempt']): a for a in attempts}
    ci = [a for a in unique.values() if Path(a['workflow'].split('@')[0]).name == 'ci.yml']
    return {'ci_runs': len({a['run_id'] for a in ci}),
            'ci_rerun_attempts': sum(a['attempt'] > 1 for a in ci),
            'required_workflow_runs': len({a['run_id'] for a in unique.values()}),
            'required_workflow_rerun_attempts': sum(a['attempt'] > 1 for a in unique.values())}


class GitHub:
    def __init__(self, max_requests):
        self.count = 0
        self.max_requests = max_requests

    def request(self, endpoint, query=None):
        if self.count >= self.max_requests:
            raise RuntimeError('GitHub request budget reached; increase --max-requests deliberately')
        self.count += 1
        args = ['gh', 'api', endpoint]
        if query:
            args += ['-f', 'query=' + query]
        return json.loads(subprocess.check_output(args, timeout=90))

    def pages(self, endpoint, key, cap, warnings):
        result = []
        for page in range(1, cap + 1):
            data = self.request(endpoint + ('&' if '?' in endpoint else '?') + f'per_page=100&page={page}')
            items = data[key] if key else data
            result.extend(items)
            if len(items) < 100:
                break
        else:
            warnings.append(f'Pagination cap reached: {endpoint}; evidence may be incomplete')
        return result


def collect(args):
    owner, name = args.repo.split('/')
    github = GitHub(args.max_requests)
    warnings = []
    prs = json.loads(subprocess.check_output([
        'gh', 'pr', 'list', '--repo', args.repo, '--state', 'merged', '--limit', str(args.limit),
        '--search', f'merged:{args.since}..{args.until}', '--json', 'number,createdAt,mergedAt,url'], timeout=90))
    raw = {'prs': [], 'runs': [], 'attempts': [], 'warnings': warnings}
    rows = []
    ancestry_cache = {}
    for item in prs:
        query = '''query { repository(owner:%s,name:%s) { pullRequest(number:%d) {
          number createdAt mergedAt headRefOid mergeCommit { oid parents(first:1) { nodes { oid } } } commits(first:100) { pageInfo { hasNextPage }
          nodes { commit { oid messageHeadline pushedDate parents(first:10) { totalCount nodes { oid } } } } }
          timelineItems(first:100,itemTypes:[HEAD_REF_FORCE_PUSHED_EVENT]) { pageInfo { hasNextPage }
          nodes { ... on HeadRefForcePushedEvent { createdAt beforeCommit { oid } afterCommit { oid } } } }
        } } }''' % (json.dumps(owner), json.dumps(name), item['number'])
        pr = github.request('graphql', query)['data']['repository']['pullRequest']
        annotate_main_parents(pr, args.repo, github, warnings, ancestry_cache)
        raw['prs'].append(pr)
        push, source = push_evidence(pr)
        rows.append({**item, 'open_to_merge_seconds': seconds(item['createdAt'], item['mergedAt']),
                     'latest_substantive_push_at': push, 'push_evidence': source,
                     'push_to_merge_seconds': seconds(push, item['mergedAt']),
                     'automatic_main_sync_commits': sum(not substantive(n['commit']) for n in pr['commits']['nodes']),
                     'attempts': []})
    # Start at earliest PR creation to include all sampled PR runs, not just merge-day runs.
    earliest = min((p['createdAt'] for p in prs), default=args.since)
    runs = []
    if prs:
        for workflow in sorted(WORKFLOWS):
            runs.extend(github.pages(
                f'repos/{args.repo}/actions/workflows/{workflow}/runs?event=pull_request&created={earliest}..{args.until}T23:59:59Z',
                'workflow_runs', args.max_pages, warnings))
    raw['runs'] = runs
    for run in runs:
        if Path(run.get('path', '').split('@')[0]).name not in WORKFLOWS or run['event'] != 'pull_request':
            continue
        matches = []
        for pr, row in zip(raw['prs'], rows):
            shas = {n['commit']['oid'] for n in pr['commits']['nodes']} | {pr['headRefOid']}
            for event in pr['timelineItems']['nodes']:
                shas.update(c['oid'] for c in [event.get('beforeCommit'), event.get('afterCommit')] if c)
            if (any(p['number'] == pr['number'] for p in run.get('pull_requests', [])) or run['head_sha'] in shas) and timestamp(run['created_at']) <= timestamp(pr['mergedAt']):
                matches.append(row)
        if not matches:
            continue
        for number in range(1, min(run['run_attempt'], args.max_attempts) + 1):
            attempt = github.request(f'repos/{args.repo}/actions/runs/{run["id"]}/attempts/{number}')
            jobs = github.pages(f'repos/{args.repo}/actions/runs/{run["id"]}/attempts/{number}/jobs',
                                'jobs', args.max_pages, warnings)
            raw['attempts'].append({'run': attempt, 'jobs': jobs})
            metric = attempt_metrics(attempt, jobs)
            for row in matches:
                sync_shas = {n['commit']['oid'] for pr in raw['prs'] if pr['number'] == row['number']
                             for n in pr['commits']['nodes'] if not substantive(n['commit'])}
                row['attempts'].append({**metric, 'trigger_kind': 'rerun' if number > 1 else
                                        ('automatic_main_sync' if run['head_sha'] in sync_shas else
                                         'new_run_substantive_or_unclassified_push')})
        if run['run_attempt'] > args.max_attempts:
            warnings.append(f'Run {run["id"]}: attempts truncated at {args.max_attempts}')
    for row in rows:
        row.update(row_run_counts(row['attempts']))
    unique = {(a['run_id'], a['attempt']): a for row in rows for a in row['attempts']}
    summary = {'sample_prs': len(rows), 'api_requests': github.count, 'warnings': warnings,
               'workflow_runs': len({a['run_id'] for a in unique.values()}),
               'rerun_attempts': sum(a['attempt'] > 1 for a in unique.values()),
               'non_success_attempts': sum(a['conclusion'] != 'success' for a in unique.values()),
               'timed_out_jobs': sum(a['timed_out_jobs'] for a in unique.values())}
    summary['workflows'] = {}
    for workflow in sorted(WORKFLOWS):
        attempts = [a for a in unique.values() if Path(a['workflow'].split('@')[0]).name == workflow]
        conclusions = {}
        for attempt in attempts:
            conclusion = attempt['conclusion'] or 'pending'
            conclusions[conclusion] = conclusions.get(conclusion, 0) + 1
        summary['workflows'][workflow] = {
            'runs': len({a['run_id'] for a in attempts}), 'attempts': len(attempts),
            'conclusions': conclusions,
            'non_success_fraction': sum(a['conclusion'] != 'success' for a in attempts) / len(attempts) if attempts else None,
            'elapsed_p50_seconds': percentile([a['elapsed_seconds'] for a in attempts], .5),
            'elapsed_p90_seconds': percentile([a['elapsed_seconds'] for a in attempts], .9),
            'execution_p90_seconds': percentile([a['execution_wall_seconds'] for a in attempts], .9),
            'queue_p90_seconds': percentile([a['queue_seconds'] for a in attempts], .9)}
    for label, values in [('open_to_merge', [r['open_to_merge_seconds'] for r in rows]),
                          ('push_to_merge', [r['push_to_merge_seconds'] for r in rows]),
                          ('workflow_queue', [a['queue_seconds'] for a in unique.values()]),
                          ('workflow_execution', [a['execution_wall_seconds'] for a in unique.values()]),
                          ('workflow_elapsed', [a['elapsed_seconds'] for a in unique.values()])]:
        summary[label] = {'observations': sum(v is not None for v in values),
                          'p50_seconds': percentile(values, .5), 'p90_seconds': percentile(values, .9)}
    return {'schema_version': 1, 'repo': args.repo, 'since': args.since, 'until': args.until,
            'summary': summary, 'prs': rows}, raw


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', default='crewship-ai/crewship')
    parser.add_argument('--since', required=True, help='merge date YYYY-MM-DD')
    parser.add_argument('--until', default=datetime.now(timezone.utc).date().isoformat())
    parser.add_argument('--limit', type=int, default=10)
    parser.add_argument('--max-pages', type=int, default=3)
    parser.add_argument('--max-attempts', type=int, default=5)
    parser.add_argument('--max-requests', type=int, default=150)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--raw-output', type=Path, required=True)
    args = parser.parse_args()
    for name in ('since', 'until'):
        if not re.fullmatch(r'\d{4}-\d{2}-\d{2}', getattr(args, name)):
            parser.error(f'--{name} must be YYYY-MM-DD')
        timestamp(getattr(args, name))
    if args.since > args.until:
        parser.error('--since must precede --until')
    if not re.fullmatch(r'[\w.-]+/[\w.-]+', args.repo):
        parser.error('--repo must be owner/name')
    if any(getattr(args, n) < 1 for n in ('limit', 'max_pages', 'max_attempts', 'max_requests')):
        parser.error('limits must be positive')
    result, raw = collect(args)
    args.raw_output.write_text(json.dumps(raw, indent=2) + '\n')
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result['summary'], indent=2))


if __name__ == '__main__':
    main()
