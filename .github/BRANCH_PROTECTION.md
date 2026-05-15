# Branch Protection Setup

`required-checks` is the single CI gate job that must pass before merge.

## Recommended Branch Rules

Apply the following rules to `main` (and `master` if still used):

1. Require a pull request before merging.
2. Require at least 1 approval.
3. Dismiss stale approvals when new commits are pushed.
4. Require status checks to pass before merging:
   - `Required Checks`
5. Require branches to be up to date before merging.
6. Restrict force pushes and branch deletions.

## Why this file exists

Workflow files define checks, but repository branch protection is a GitHub setting.
This doc keeps the "PR gate" policy explicit and reviewable in-repo.
