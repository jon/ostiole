#!/usr/bin/env python3
"""Exercise workflow commands with local repositories and a fake GitHub API."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


def step_script(name):
    lines = (ROOT / '.github/workflows/test.yml').read_text().splitlines()
    start = lines.index('      - name: ' + name)
    for index in range(start + 1, len(lines)):
        if lines[index] in ('        run: |', '        run: >-'):
            body = []
            for line in lines[index + 1:]:
                if line.strip() and not line.startswith('          '):
                    break
                body.append(line[10:])
            separator = ' ' if lines[index].endswith('>-') else '\n'
            return separator.join(body)
    raise ValueError('missing multiline run block: ' + name)


class PublicationGuards(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        self.git('init', '-q')
        self.git('config', 'user.name', 'Test')
        self.git('config', 'user.email', 'test@example.invalid')
        self.env = os.environ | {
            'GITHUB_WORKSPACE': str(self.repo),
            'RUNNER_TEMP': str(self.root),
            'GITHUB_EVENT_PATH': str(self.root / 'event.json'),
        }

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.repo, text=True).strip()

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'Establish the fixture.')
        return self.git('rev-parse', 'HEAD')

    def run_step(self, name):
        return subprocess.run(
            ['bash', '-eo', 'pipefail', '-c', step_script(name)],
            cwd=self.repo, env=self.env, text=True, capture_output=True,
        )

    def test_invalid_commit_range_fails(self):
        self.env |= {'BASE_SHA': 'not-a-revision', 'HEAD_SHA': 'HEAD'}
        result = self.run_step('Test every pull-request commit')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_review_evaluator_uses_base_checkout(self):
        workflow = (ROOT / '.github/workflows/test.yml').read_text()
        review = workflow.split('  codex-reviewed:', 1)[1]
        checkout = review.split('      - name: Check out the review policy', 1)[1]
        checkout = checkout.split('      - name: Wait for Codex', 1)[0]
        self.assertIn('ref: ${{ github.event.pull_request.base.sha }}', checkout)
        self.assertNotIn('github.event.pull_request.draft', review)

    def test_empty_commit_range_fails(self):
        (self.repo / 'fixture').write_text('base')
        sha = self.commit()
        self.env |= {'BASE_SHA': sha, 'HEAD_SHA': sha}
        result = self.run_step('Test every pull-request commit')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_checker_uses_base_code_and_head_data(self):
        (self.repo / 'go.mod').write_text('module fixture\n\ngo 1.25\n')
        checker = self.repo / 'internal/ci/checkpr/main.go'
        checker.parent.mkdir(parents=True)
        checker.write_text('''package main
import ("flag"; "os"; "path/filepath")
func main() {
 repo := flag.String("repo", ".", "")
 flag.String("base", "", ""); flag.String("head", "", "")
 flag.String("event", "", ""); flag.Parse()
 data, err := os.ReadFile(filepath.Join(*repo, "proposal"))
 if err != nil || string(data) != "head data" { os.Exit(2) }
 os.Exit(1)
}
''')
        (self.repo / 'proposal').write_text('base data')
        base = self.commit()
        checker.write_text('package main\nfunc main() {}\n')
        (self.repo / 'proposal').write_text('head data')
        head = self.commit()
        self.git('worktree', 'add', '--detach', str(self.root / 'policy-base'), base)
        self.env |= {'BASE_SHA': base, 'HEAD_SHA': head}
        result = self.run_step('Check commits and pull-request metadata')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)


    def run_review(self, mode):
        scripts = self.repo / '.github/scripts'
        scripts.mkdir(parents=True)
        (scripts / 'codex-review-signal.jq').write_text(
            (ROOT / '.github/scripts/codex-review-signal.jq').read_text()
        )
        tools = self.root / 'tools'
        tools.mkdir()
        fake = tools / 'gh'
        fake.write_text("#!/usr/bin/env python3\n" + FAKE_GH)
        fake.chmod(0o755)
        sleeper = tools / 'sleep'
        sleeper.write_text('#!/bin/sh\nexit 0\n')
        sleeper.chmod(0o755)
        self.env |= {
            'PATH': str(tools) + os.pathsep + self.env['PATH'],
            'GH_REPO': 'owner/repo', 'PR_NUMBER': '53',
            'HEAD_SHA': '0123456789abcdef0123456789abcdef01234567',
            'GUARD_TEST_MODE': mode, 'GUARD_TEST_STATE': str(self.root / 'state'),
        }
        return self.run_step('Wait for Codex to review the current head')

    def test_draft_cannot_pass_review(self):
        result = self.run_review('draft')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_superseded_head_cannot_pass_review(self):
        result = self.run_review('superseded')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_transient_timeline_failure_retries(self):
        result = self.run_review('transient')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_fork_run_is_attributed(self):
        result = self.run_review('fork')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_wrong_fork_repository_cannot_pass(self):
        result = self.run_review('wrong-fork')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)


FAKE_GH = r"""
import json
import os
from pathlib import Path
import sys

endpoint = next(arg for arg in sys.argv[1:] if arg.startswith('repos/'))
mode = os.environ['GUARD_TEST_MODE']
head = os.environ['HEAD_SHA']
actor = 'chatgpt-codex-connector[bot]'
if '/pulls/' in endpoint:
    print(json.dumps({'draft': mode == 'draft', 'head': {
        'sha': 'f' * 40 if mode == 'superseded' else head,
        'repo': {'id': 1234},
    }}))
elif '/commits/' in endpoint:
    print(head)
elif '/timeline?' in endpoint:
    if mode == 'transient':
        state = Path(os.environ['GUARD_TEST_STATE'])
        if not state.exists():
            state.touch()
            sys.exit(1)
    entries = [] if 'fork' in mode else [
        {'event': 'committed', 'sha': head},
        {'event': 'commented', 'user': {'login': actor},
         'body': "Codex Review: Didn't find any major issues.\n**Reviewed commit:** `" + head + '`'},
    ]
    print(json.dumps([entries]))
elif '/comments?' in endpoint:
    rows = [] if 'fork' not in mode else [{
        'user': {'login': actor},
        'body': '<!-- codex-pull-request-review-summary -->\n'
                '| Review | Status | Commit |\n'
                '| 📝 **Code Review** | ✅ **Completed** | `' + head[:7] + '` |',
    }]
    print(json.dumps([rows]))
elif '/reactions?' in endpoint:
    print(json.dumps([[{'user': {'login': actor}, 'content': '+1',
                       'created_at': '2026-08-29T20:29:46Z'}]]))
elif '/actions/runs?' in endpoint:
    print(json.dumps([{'workflow_runs': [{
        'head_sha': head, 'created_at': '2026-08-29T20:29:30Z',
        'pull_requests': [],
        'head_repository': {'id': 5678 if mode == 'wrong-fork' else 1234},
    }]}]))
else:
    sys.exit('unexpected endpoint: ' + endpoint)
"""


if __name__ == '__main__':
    unittest.main()
