---
title: Run a check
description: "What slopcop sends, how the base commit is chosen, the flags that shape a check, and what happens when the patch is empty."
sidebar:
  order: 1
---

```console
slopcop                          # check the change in the current checkout
slopcop check --if-enabled       # automatic check: skip a repository disabled here
slopcop check --format json      # same, machine-readable
slopcop --root ../repo --base origin/main
```

Running `slopcop` with no subcommand runs the check; `slopcop check` is the same
command. The static half sends one request and its result comes from the
service. The local review has a separate result. `--if-enabled` can skip a
repository the workspace has disabled; that is a `skipped` result, never a
clean one.

## Local review

By default, `slopcop check` runs the hosted static check and local review
concurrently, once reviewers are configured (`slopcop init` picks them). With
none configured, or in a CI job, it runs only the static check. `--mode static` runs only the hosted check; `--mode ai` runs only
review without a SlopCop login, enabled repository, or supported origin.
`--static` and `--ai` are shortcuts. The review uses Node.js with `npx` and
existing harness logins. The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox. A review finding is a model's judgment, while a static finding traces to a rule.

### Where reviewers start

Each reviewer starts in its own private directory under your user cache, so
neither the repository under review nor files another account leaves in a
shared temp directory can choose what `npx` runs. A review stops with an error
if the user cache is inside that repository or below a directory another
account can write to. To fix it, point `XDG_CACHE_HOME` on Linux, `HOME` on
macOS, or `LocalAppData` on Windows at a private directory. A review also stops
if another account can change a `node_modules` or `node_modules/.bin` directory
above the cache. Remove that folder or restrict its access.

These checks do not cover every setup on a machine shared with other accounts.
They do not inspect individual files in a `node_modules/.bin` above the cache,
or the parents of a directory that `.bin` links to. On Windows, they do not
protect the drive of an npm that `npx.cmd` selects from another prefix. Keep
those locations writable only by your account.

### Local review on Windows

By default, any signed-in Windows account can create folders in `C:\`. npm and
Node both look in `C:\node_modules`, so a folder another account put there
could change what a reviewer runs. The first time SlopCop starts a reviewer, it
creates an empty `C:\node_modules` that only your account, SYSTEM, and
Administrators can change. If your user cache or Node.js is on another drive,
it does the same on that drive.

With that folder in place, npm treats `C:\` as the project when you run it in a
folder with no `package.json`, so `npm install <package>` there installs into
`C:\node_modules`. Run `npm init -y` in the folder first to keep the install
local.

On a machine with more than one account, the folder belongs to the account that
ran a review first, and SlopCop refuses to start reviewers for the others. To
share it, have an administrator replace it with one owned by Administrators. In
Command Prompt, run as administrator:

```bat
rmdir C:\node_modules
mkdir C:\node_modules
icacls C:\node_modules /setowner *S-1-5-32-544
icacls C:\node_modules /inheritance:r /grant:r *S-1-5-18:(OI)(CI)F *S-1-5-32-544:(OI)(CI)F *S-1-5-11:(OI)(CI)RX
dir /a C:\node_modules
```

This gives SYSTEM and Administrators full control and every signed-in account
read access. `rmdir` fails if the folder does not exist, which is fine, or if
it has contents. Look at any contents before you remove them, because another
account may have put them there. The last command should list no files or
folders other than `.` and `..`. If your user cache or Node.js is on another
drive, run the same commands there with that drive's letter.

In a folder where other accounts can add folders, such as `C:\`, an existing
`node_modules` is accepted only if no other account can change it or anything
inside it. Otherwise reviewers do not start, and the error names the path.

## What is sent

In modes with static analysis, one git patch travels from the base commit to the
working tree. The local reviewers read the captured tree diff. The patch carries
committed but unpushed work, unstaged edits, untracked files, deletions,
renames, mode changes, and binary content. Git-ignored files stay out, except
selected `.slopcop/policies` rules, which must travel because the service
evaluates them from the patched checkout.

Before generating either the upload or local review diff, the CLI omits changed
JSON data (`.json`, `.jsonl`, `.ndjson`, case-insensitive) and Git-detected binary
files when either version exceeds **500,000 bytes (0.5 MB)**. Smaller JSON
configuration files and other text/source files remain in scope. The check uses
the captured Git blobs, including the old version for deletions and both sides
of a rename, and leaves your files and index unchanged.

Changed files that `.bifrostignore` or `[check] exclude` in
`.slopcop/config.toml` keep out of every check stay out of the upload and the
local review too, whatever their size: the service would not check them
anyway. `--include` does not bring them back. The service then analyzes the
rest of the change against the base versions of those files, so keep files
the analysis reads, such as a `Cargo.toml` or `package.json`, out of
`[check] exclude`. The selected `.slopcop/policies`
rules, `.slopcop/config.toml` and the `.bifrostignore` files themselves are
always sent. A deleted file carries no content: the patch names the blob it
deletes, so removing a large folder costs a few lines per file, and the
service still deletes a file only when its content matches.

Omitted files are unreviewed. Human output names each path with its size and
reason, plus the total count and size; for excluded files it gives the count
and size and names the first ten. JSON always lists `omittedFiles` and
SARIF carries `slopcop.omittedFiles`. A passing check covers the retained scope;
the full snapshot remains the push identity. If every change is omitted, the
check is `skipped`: nothing is sent or analyzed and no push approval is recorded.

Opt particular data files back in with repeatable paths or globs:

```console
slopcop check --include config/settings.json --include 'models/**/*.json'
```

Patterns are repository-relative. `*` matches within a directory and `**`
matches across directories; quote globs to keep shell expansion out of the
selection. An inclusion overrides only the size filter, so other changed source
files still run through the check. Including either path of a rename includes
the complete rename.

A genuinely unchanged tree is still sent as an empty patch. The service's
overall payload and file-count limits still apply; its `payload_too_large`
response is printed verbatim if the remaining patch exceeds them. An upload
refused with HTTP 413 before the service could answer is reported as
`payload_too_large` too, with the request's size.

With the patch go the repository's URL (canonicalized from the `origin`
remote), the base commit's full hash, the policy selection, and the `--fail-on`
level. Run from a Claude Code or Codex session, a check also sends which models
wrote each changed file. [What leaves your machine](/account/privacy/) lists
every field.

## The base commit

The base is the commit the service applies the patch to. It must already be in
the service's clone, which is why it should be a commit origin has. By default
the CLI resolves it in this order:

1. the merge base with the branch's upstream;
2. when that gives an empty patch, as on a branch whose commits are all pushed,
   the merge base with `origin/HEAD`, so a check right after a push still
   covers the branch;
3. `HEAD`, when neither is known.

On the default branch with nothing unpushed, the base is `HEAD` and the patch
is empty.

### Choose the base yourself

| Flag | Meaning |
| --- | --- |
| `--base REV` | The branch or revision the change started from. Like `git diff REV...HEAD`, the base is the merge base of `REV` and `HEAD`, so commits that landed on `REV` after the branch point are not sent as reverse edits. When a branch stacked on `REV` has since merged a newer `origin/HEAD` or `REV`'s upstream, and that line already holds `REV`'s merge base (as once `REV` has landed), the base moves to that line's merge base so the merged-in commits are not sent. When the lines have diverged, the base stays and the check warns that the patch carries them, and suggests merging the line into `REV`. Stops with an error when `REV` shares no history with `HEAD`. |
| `--base-exact REV` | That exact commit, without a merge base. A target branch that moved on then shows up as reverse edits. |

The two cannot be combined. When you check work you have already committed,
pass `--base` with the branch the change targets, such as the pull request's
base:

```console
slopcop --base origin/main
```

A base the service's clone lacks comes back as `base_not_found` with its
remedy. The CLI then asks origin for its default branch (`git ls-remote`, no
prompt, ten-second limit) and adds a line when the clone explains the failure:
origin's default branch was renamed and `origin/HEAD` still names the old one,
or the base is not on origin's default branch. Follow that line.

### The first push of a new repository

Before anything is pushed to a new repository, origin has no branches, so the
service cannot read any commit of it and the static check answers
`base_not_found`. Pushing the base, its usual remedy, is what the push gate
blocks until a check approves the tree. The line after the remedy says so and
gives the way through: while origin has no branches, a passing review-only
check approves the tree on its own, and stderr says `approved without a static
check`.

```console
slopcop check --mode ai --base-exact <first-commit>
git push -u origin main
slopcop check --mode static --base-exact <first-commit>
```

The last check runs the static half on the same change once the service can
read the base. A repository with a single commit has nothing to check that
commit against, so no check can approve it; the hint says so, and the way
through is a person pushing it from a terminal, since the gate guards agents. After the first push, a review-only check no longer approves a
tree; see [which results approve the tree](/checks/exit-codes/#which-results-approve-the-tree).

## An empty patch

An empty patch is a real check, not a skip: one request goes out and the
service answers. The report then has `"emptyChange": true` (`sarif`:
`properties["slopcop.emptyChange"]`), and stderr says so and suggests `--base`.
A `clean` status on an empty change means nothing was checked, not that the
work passed.

## Flags

| Flag | Meaning |
| --- | --- |
| `--root PATH` | repository to check (default `.`) |
| `--base REV` | branch or revision the change started from; the base is its merge base with `HEAD` |
| `--base-exact REV` | exact base commit or revision, without a merge base |
| `--format human\|json\|sarif` | report format (default `human`); see [Output formats](/checks/output-formats/) |
| `--fail-on never\|finding\|note\|warning\|error` | exit-1 threshold for new findings (default the repository's `[check] fail_on`, else `finding`) |
| `--certainty definite\|all` | report and fail on only `definite` findings (default), or on `possible` and `heuristic` ones too |
| `--policy PATH` | repository-relative `.rqlp` file to evaluate (repeatable); see [Repository rules](/checks/repository-rules/) |
| `--no-policy-discovery` | skip `.slopcop/policies` rules |
| `--policy-id ID`, `--policy-pack PACK`, `--policy-category CATEGORY` | run only these built-ins instead of the default catalog (each repeatable); see [Repository rules](/checks/repository-rules/#select-rules-by-hand) |
| `--timeout DURATION` | how long to wait for the result (default `10m`); a check still running then exits `3` |
| `-w`, `--workspace NAME\|ID` | check in, and bill, this workspace of the account instead of the login's; see [Workspaces](/account/workspaces/) |
| `--if-enabled` | skip a repository the workspace has disabled or has past its active limit: exit `0` and send nothing. One it has never added is added. For automatic checks; see [Repositories](/account/repositories/) |
| `--service URL` | the check service (also `SLOPCOP_SERVICE`) |
| `--verbose` | print the resolved request to stderr, and in `human` name each inconclusive check and the files and rules each [`.slopcop/config.toml` exclusion](/checks/false-positives/#exclude-paths-from-checks) resolved to |

### The threshold

The CLI always sends `--fail-on`. Without the flag it sends the repository's
threshold, `fail_on` in the `[check]` section of `.slopcop/config.toml` in the
checked tree, the one its pull request checks apply (see
[The repository's threshold](/checks/exit-codes/#the-repositorys-threshold)).
When the repository sets none it sends `finding`: a check fails only on
findings the change introduced, so any of them, a note or an unrated one
included, is worth fixing before the change lands. The service's own default
for a request without a level is Bifrost's `warning`; pass `--fail-on warning`
to match it. New findings that cross the threshold exit `1` even when some
rule did not complete.

The threshold decides only the exit code. The report's `status` is `findings`
whenever the change has new findings, so `--fail-on never` reports
`"status": "findings"` and exits `0`, and `--fail-on error` does the same for
a change whose new findings are all warnings or notes.

The threshold applies to `definite` findings only. By default the service
leaves out every `possible` and `heuristic` finding before it decides the
status, never sends them, and counts them in `summary.lessCertainFindings`,
which the summary line prints. `--certainty all` asks for every finding and
lets any of them fail the check. The automatic check run by agent hooks always
uses the default.

## Waiting for the result

While the analysis runs, the service answers `check_running` and the CLI waits
the interval it names and asks again, until `--timeout` runs out. It asks by
the check's id, so the change is sent only once, and every ask attaches to the
same running check. If it is still running then, the CLI exits
`3`, prints the service's message and remedy on stderr, and in `json` prints a
`pending` report. Running the same check again picks up the same analysis: an
identical request by the same login within an hour attaches to it rather than
starting a new one.

A check that is interrupted (Ctrl-C, or SIGTERM) prints no report and exits
`130` or `143`. Interrupted before it was sent, nothing reached the service;
after, the service keeps running it, and running the same check again picks up
its result.

## A check of a repository that is not enabled

A check of a repository the workspace has never added adds it to the workspace
the check runs in, which then pays for its checks, and says so on stderr. A
repository the workspace disabled exits `4` with `repository_disabled`, and one
past the active limit with `active_repository_limit_reached`, without sending
anything or starting the local reviewers; only a person turns it back on. An
automatic check can pass `--if-enabled` to exit `0` and send nothing instead.
[Repositories](/account/repositories/) explains adding and the active limit.
A private repository needs the workspace's
[GitHub App, GitLab, or Bitbucket connection](/account/private-repositories/)
first.
