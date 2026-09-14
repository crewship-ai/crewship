#!/usr/bin/env python3
"""Keep the existing self-update grammar while giving retries distinct versions."""
from datetime import datetime, timezone
import os


def version(run, attempt, date):
    # Old clients parse the numeric suffix as a native int (including 32-bit ARM).
    # Reserve two decimal places for attempts, and fail rather than collide.
    if not 1 <= attempt <= 99 or not 1 <= run <= 21_474_835:
        raise ValueError('Nightly run/attempt exceeds the compatible version range')
    return f'nightly-{date}-r{run * 100 + attempt}'


if __name__ == '__main__':
    value = version(int(os.environ['GITHUB_RUN_NUMBER']), int(os.environ['GITHUB_RUN_ATTEMPT']),
                    datetime.now(timezone.utc).strftime('%Y%m%d'))
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        print(f'version={value}', file=output)
