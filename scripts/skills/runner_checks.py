#!/usr/bin/env python3
"""Select existing runner lifecycle tests by concern; print commands unless --run is supplied."""
import argparse
import json
from pathlib import Path
import re
import subprocess
import sys

PROFILES = {
    'lease': [('./internal/runnerruntime', '^Test(AttemptSupervisor|LeaseRenewer|LeaseWatcher|RunnerStartup)')],
    'replay': [('./internal/runnerruntime', '^Test(ReconcileRunnerJournal|AttemptReporter|JournaledCompletion|RunnerStartup)')],
    'journal': [('./internal/runner', '^TestAttemptJournal')],
    'shutdown': [('./internal/runner', '^Test(ExecuteProcess|ReceiveProcessWait|CleanRemainingProcessGroup|TerminateProcessGroup)'),
                 ('./internal/runnerruntime', '^Test(AttemptSupervisor|SupervisorCancellation|LeaseRenewer|LeaseWatcher|DeploymentStatusWatcher|RollbackComposeCancellation|RollbackComposeOperationRetainsSupervisorCancellation)')],
}


def commands(profile, race=False):
    return [['go', 'test', *(['-race'] if race else []), '-count=1', '-run', pattern, package]
            for package, pattern in PROFILES[profile]]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('profile', choices=PROFILES)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--run', action='store_true')
    args = parser.parse_args()
    plan = commands(args.profile, args.race)
    if not args.run:
        print(json.dumps({'status': 'not_run', 'commands': plan}, indent=2))
        return 0
    root = Path(__file__).resolve().parents[2]
    # Fail before executing the plan if a rename makes a group select no tests.
    for package, pattern in PROFILES[args.profile]:
        listing = subprocess.check_output(['go', 'test', '-list', pattern, package], cwd=root, text=True)
        if not any(re.match(pattern, line) for line in listing.splitlines()):
            raise ValueError(f'no tests match {package}: {pattern}')
    for command in plan:
        result = subprocess.run(command, cwd=root)
        if result.returncode:
            return result.returncode
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f'runner checks: {error}', file=sys.stderr)
        sys.exit(2)
