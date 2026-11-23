# SlopCop

**Hosted static analysis and local review for every change.**

[![CI](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml/badge.svg)](https://github.com/BrokkAi/slopcop/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/BrokkAi/slopcop)](https://github.com/BrokkAi/slopcop/releases/latest)
[![npm](https://img.shields.io/npm/v/@brokkai/slopcop)](https://www.npmjs.com/package/@brokkai/slopcop)
[![PyPI](https://img.shields.io/pypi/v/brokk-slopcop)](https://pypi.org/project/brokk-slopcop/)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-blue.svg)](https://go.dev/)

In its default `both` mode, `slopcop` sends one git patch, from the base commit
to the working tree, to the hosted check API at <https://slopcop.com>. The
service applies it, runs the hosted policy catalog plus the repository rules you
selected, and returns the report: every finding with its severity, the file and
line that triggered it, and the certainty behind it. The analysis is
[Bifrost](https://github.com/BrokkAi/bifrost), an open-source static analysis
engine, so a static finding traces back to a rule. Local review findings are
model judgments that need inspection.

The full documentation is at <https://docs.slopcop.com>.

The hosted static analyzer needs no per-language install. Local review uses your
existing harness logins and Node.js with `npx`. `--mode ai` needs no SlopCop
login, enabled repository, or supported origin. Use `--mode static` for a
hosted-only check or CI without reviewer harness logins. A check of a
repository the workspace has never added adds it; `--if-enabled` skips one the
workspace has disabled, and says it was skipped.

## Install

### One command

On macOS or Linux, one command installs the CLI, signs it in, and adds its
agent skill and hooks. Sign in at <https://slopcop.com>, open **Agent setup**,
choose **Create install command**, and run what it gives you:

```console
curl -fsSL https://slopcop.com/install/<your code> | sh
```

It takes the latest release, verifies the archive against `checksums.txt`,
installs `slopcop` to `$HOME/.local/bin` and adds that directory to the shell
profile, signs in to the workspace you made the command in, and runs
`slopcop init`. The code is yours: it signs in once and expires after 30
minutes, so don't share the command. Running it again later only upgrades the
CLI, and keeps the login the machine has. `SLOPCOP_VERSION`,
`SLOPCOP_INSTALL_DIR`, and `SLOPCOP_NO_MODIFY_PATH` override its defaults.

Without a code, `curl -fsSL https://slopcop.com/install | sh` does the same and
signs in through the browser device flow instead (see [Sign in](#sign-in)).
Then open your agent in any repository you want checked: its first check adds
the repository to your workspace (repositories are free; a check uses a free
check, then credits). `slopcop enable` is optional; see [Account](#account).

### Other channels

npm, PyPI, the release archive, and the shell and PowerShell installers all
install the same release binary for macOS, Linux, and Windows on x64 and
arm64, so `slopcop version` reports which release it came from. The Go
toolchain can build that binary from source instead.

| Channel | Platforms | Needs |
| --- | --- | --- |
| [One command](#one-command) | macOS, Linux | curl, a slopcop.com account |
| [npm](#npm) | macOS, Linux, Windows | Node 22 or newer |
| [PyPI](#pypi) | macOS, Linux, Windows | Python 3.9 or newer |
| [Release archive](#release-archive) | macOS, Linux, Windows | nothing |
| [Shell installer](#shell-installer) | macOS, Linux | curl |
| [PowerShell installer](#powershell-installer) | Windows | PowerShell |
| [Go](#go) | built from source | Go 1.25 or newer |

### npm

```console
npm install --global @brokkai/slopcop
slopcop --help
```

The tarball carries the release binary for every supported platform, so it
needs no Go toolchain. `npx @brokkai/slopcop` runs it without installing it.

### PyPI

```console
pip install brokk-slopcop
```

`pipx install brokk-slopcop` or `uv tool install brokk-slopcop` puts it in an
environment of its own. The wheels carry the release binary, so no Go toolchain
is needed either.

The package is [`brokk-slopcop`](https://pypi.org/project/brokk-slopcop/),
published from this repository. Do not run `pip install slopcop`: PyPI's
`slopcop` is an unrelated project by someone else. The command `brokk-slopcop`
installs is still `slopcop`, and `python -m brokk_slopcop` runs the same binary.

### Release archive

Each release publishes `slopcop_v<version>_<os>_<arch>.tar.gz` (`.zip` on
Windows) holding the binary, `README.md`, and `LICENSE`, plus a `checksums.txt`
covering every archive. Download them from
<https://github.com/BrokkAi/slopcop/releases/latest> and check the sha256
before unpacking:

```console
ver=0.0.3; os=linux; arch=amd64   # darwin/arm64 on Apple silicon
base=https://github.com/BrokkAi/slopcop/releases/download/v$ver
curl -fsSLO "$base/slopcop_v${ver}_${os}_${arch}.tar.gz"
curl -fsSLO "$base/checksums.txt"
grep "${os}_${arch}" checksums.txt | sha256sum -c -   # shasum -a 256 -c - on macOS
tar -xzf "slopcop_v${ver}_${os}_${arch}.tar.gz" slopcop
sudo install -m 755 slopcop /usr/local/bin/slopcop
```

### Shell installer

```console
curl -fsSL https://raw.githubusercontent.com/BrokkAi/slopcop/master/install.sh | sh
```

It takes the latest release, verifies the archive against `checksums.txt`,
installs `slopcop` to `$HOME/.local/bin`, and adds that directory to the
shell profile. `SLOPCOP_VERSION`, `SLOPCOP_INSTALL_DIR`, and
`SLOPCOP_NO_MODIFY_PATH` override its defaults.

### PowerShell installer

```powershell
irm https://raw.githubusercontent.com/BrokkAi/slopcop/master/install.ps1 | iex
```

It takes the latest release, verifies the archive against `checksums.txt`,
installs `slopcop.exe` to `%LOCALAPPDATA%\Programs\slopcop`, and adds that
directory to the user `Path`. `SLOPCOP_VERSION`, `SLOPCOP_INSTALL_DIR`, and
`SLOPCOP_NO_MODIFY_PATH` override its defaults.

### Upgrade notice

Every response from the service names its newest release in the
`SlopCop-Latest-Version` header. When that is newer than the running build,
the CLI prints one line on stderr, never stdout, so `--format json` and
`sarif` stay clean:

```text
slopcop v0.0.9 is available (you have v0.0.7). Upgrade: npm install --global @brokkai/slopcop@latest
```

The command follows the channel the binary was installed through (npm, npx,
pip, pipx, or uv), judged by where it lives, and links back here otherwise.
The notice prints at most once a day; the time of the last one is kept in
`slopcop/update-notice` next to the config. `dev` builds, `Stop` hook runs, and
runs with `$SLOPCOP_NO_UPDATE_NOTICE` set to any non-empty value never print
it. A service that no longer accepts a version answers `client_outdated`
(HTTP 426), which exits `2`.

### Automatic updates

A slopcop installed by the install script, or by hand from a release archive,
updates itself. When the service reports a newer release, the command first
finishes its own work, then downloads that release, checks the archive against
its `checksums.txt`, and replaces its own binary:

```text
slopcop: updating from v0.0.7 to v0.0.9...
slopcop: updated from v0.0.7 to v0.0.9; the next run uses it.
```

The line goes to stderr. An update that fails never fails the command: it
prints the upgrade notice with the reason, and is tried again a day later.
`slopcop update` does the same on request. A slopcop installed through npm,
pip, pipx or uv is not replaced, because that tool owns the file; it gets the
notice above, and `slopcop update` names the command to run. Nothing updates
itself when `$SLOPCOP_NO_AUTO_UPDATE` or `$CI` is set, so a job keeps the
version it installed, or in a `Stop` hook run. Only a release build is replaced:
a `dev` build, or one built ahead of a release, stays as built, and a
pre-release is never installed by itself.

### Go

```console
go install github.com/BrokkAi/slopcop/cmd/slopcop@latest
```

Go 1.25 or newer is required to build. A plain `go install` reports `dev` as
its version; in a clone, `make install` stamps the version from `git describe`.

## Set it up in an agent

The binary carries two agent skills. `slopcop` teaches a coding agent how
to check a change. `slopcop-audit` audits code that has already landed: it
splits a commit range into module slices and checks them in parallel with
hosted checks, then triages the findings into a fix plan. The binary also
carries automatic check and push-gate hooks for Claude Code, Codex and Grok.
Run `slopcop init` once for the machine. It installs both skills into the
user-level directories below and names what it did:

| Location | Harness |
| --- | --- |
| `~/.agents/skills/slopcop/`, `~/.agents/skills/slopcop-audit/` | Codex, Grok and other agents |
| `~/.claude/skills/slopcop/`, `~/.claude/skills/slopcop-audit/` | Claude Code and other agents |

It leaves tracked files alone. A command run inside a checkout also removes
the hooks, skills and `.git/info/exclude` entries written by the earlier
per-repository releases. Other existing checkouts migrate themselves the first
time their old hook runs the upgraded binary, so setup does not have to be
repeated repository by repository.

A skill that is already there is left alone by init, so running it again is
safe. A check updates copies already installed, but creates none; login
does not install the skill. An existing copy is rewritten when it differs from
the skill bundled into the binary, so an agent follows the instructions of the
slopcop version it runs. Each install records the version that wrote it, and
what each file held, in `.slopcop-skill.json` in the skill folder. An update
names the version it replaces, the new one and the files that changed, for
example:

```
slopcop: updated the slopcop skill at .claude/skills/slopcop from v0.0.9 to v0.0.10 (changed: SKILL.md). Reread it if you loaded the old one.
```

It removes the files the new skill no longer has. A skill with local edits,
one that matches neither its record nor any release, is copied first to
`skill-backups/<harness>-<time>` next to the slopcop configuration, outside
every skills directory so no harness loads it as a second skill, and the
notice says where. A check's `json` and `sarif` reports carry the same update
(see below). `slopcop init --no-skill` skips the install and any update of
it.

Init also adds a static-only `Stop` hook and a `PreToolUse` push gate for
Claude Code, Codex and Grok, and leaves everything else in each file as it was:

| File | Harness |
| --- | --- |
| `settings.json` under `CLAUDE_CONFIG_DIR`, or `~/.claude` | Claude Code |
| `hooks.json` under `CODEX_HOME`, or `~/.codex` | Codex |
| `hooks/slopcop.json` under `GROK_HOME`, or `~/.grok` | Grok |

The push gate blocks `git push`, `gh pr create`, and the GitHub MCP tools that
push files or open a pull request until HEAD's tree has a passing check or
valid override. It is installed machine-wide but only enforces a repository
after SlopCop has created review state there. A repository that has never been
checked is left alone.

Codex runs a new or changed user-level hook only once you trust it: open Codex
and review it with `/hooks`. Init never writes that trust itself. The hook
stays the same from one release to the next, so an update does not ask for it
again.

When the agent stops, the hook checks the working tree through the same
hosted API, but only when all of these hold:

- the session wrote files in the repository with its edit tools (a Codex
  session's subagents included), or the working tree has a changed
  dependency manifest or lockfile (see below);
- slopcop is signed in, and the workspace a check would use
  (`$SLOPCOP_WORKSPACE`, the one the checkout is mapped to, or the login's) has the repository active
  (`slopcop repo list`) or has never added it, in which case the first check
  adds it. Nothing is sent for a repository the workspace has disabled or has
  past its active limit;
- the tree differs from the last check in the session, including a check the
  agent ran by hand.

An agent often adds a dependency through its shell, with `npm install`,
`pnpm add`, `cargo add` or `uv add`, which no edit tool records. So the hook
also checks when git shows one of these files changed, added or untracked in
any directory of the repository: `package.json`, `package-lock.json`,
`npm-shrinkwrap.json`, `pnpm-lock.yaml`, `yarn.lock`, `Cargo.toml`,
`Cargo.lock`, `pyproject.toml`, `uv.lock`, `poetry.lock`, `requirements*.txt`,
`go.mod` and `go.sum`. Files git ignores and those under `node_modules` do
not count. The hook recognises these files by name only: it never reads
them, runs a package manager or asks an advisory database; the hosted check
reads them like any other file in the change. A changed dependency file
counts as one the session wrote, since the hook cannot tell which command
changed it, so its new findings are handed to the agent like those in files
it edited, even if the file was already changed when the session began.

New findings in files the session wrote are handed to the agent to fix, once
for a given tree and at most twice in a row; then the agent is let go and you
are told what remains. A finding ignored with `slopcop ignore` is not handed
back. New findings in other files on the branch are only mentioned. The hook
never keeps the agent going on its own failures: a refusal the service calls
final, such as an expired login, is reported once and the hook stays quiet for
the rest of the session, until a check by hand completes; a brief outage is
only reported. The hook runs as `slopcop hook claude || true` (and
`slopcop hook codex || true`, or `|| exit /b 0` under Windows' `cmd`), so even
a `slopcop` that cannot run it, such as an older one first on `PATH`, lets the
agent stop; init warns when that is the case, because no check would run.

Use `slopcop init --no-hooks` to install only the skill, or
`slopcop init --remove-hooks` to remove SlopCop's hooks and nothing else.
Ordinary checks do not reinstall a removed hook. The skill remains the way to
check from other harnesses.

The hook checks what an agent writes and hands findings back. Its first check
of a repository the workspace has never added adds it, as any check does; it
sends nothing for one the workspace has disabled or has past its active limit.

### Managed rollout

An administrator can deploy the `Stop` hook through managed configuration
instead of having each user run `slopcop init`. Device management must still
put `slopcop` on `PATH`, and each user still signs in.

- **Claude Code:** add the handler `slopcop init` writes to the `hooks` key of
  the managed settings (`managed-settings.json` or a file in
  `managed-settings.d/`): a `Stop` group with `"type": "command"`,
  `"command": "slopcop hook claude || true"`, `"timeout": 720` and
  `"statusMessage": "Checking the change with SlopCop"`. Under
  `allowManagedHooksOnly` the user-level hooks init writes to
  `settings.json`, the push gate included, are blocked, so the managed hook is
  the only one that runs. Claude Code runs
  an identical handler from several settings files once.
- **Codex:** define the `Stop` hook under `[hooks]` in the managed
  `requirements.toml` (`command = "slopcop hook codex || true"`,
  `command_windows = "slopcop hook codex || exit /b 0"`, `timeout = 720`).
  Managed hooks are trusted by policy, so users skip `/hooks`. Codex installs
  nothing from `managed_dir`.

To keep the push gate, deploy its `PreToolUse` handler the same way
(`"command": "slopcop gate --hook claude"`, or `slopcop gate --hook codex`,
with no `|| true`); the Managed rollout page has both.

Users then run `slopcop init --no-hooks` to install only the skill. See
[Managed rollout](https://docs.slopcop.com/agents/managed-rollout/).

## Sign in

```console
slopcop login                           # browser device flow
slopcop login --token "$SLOPCOP_TOKEN"  # existing credential, for CI and agents
```

The [one-command install](#one-command) signs in for you: its code is a login
already approved in your browser, so it needs neither.

The browser flow prints a link that opens the approval page with the code
filled in, plus the page and code to type in instead, and stores the token once
the login is approved. Approve only if the page shows the code your terminal
printed.

The service URL defaults to `https://slopcop.com`; `--service` or
`$SLOPCOP_SERVICE` names another for one command. Each service keeps its own
login: `slopcop --service <url> login` leaves the others signed in and the
default as it is, unless the default has no login (as on a machine's first
login), in which case that service becomes the default and login says so. A login is only sent to the service that
issued it.
`slopcop login --default` or `slopcop service use <url>` changes the default,
after asking at a terminal, and `slopcop service list` shows the stored logins. A command that talks to
another service than `https://slopcop.com` says so on stderr.

The credentials live in `slopcop/config.json` under the user config directory,
which is written owner-only. `slopcop logout` forgets the login for one
service, and `slopcop logout --all` every one.

Precedence:

| Value | Order |
| --- | --- |
| Service URL | `--service`, `$SLOPCOP_SERVICE`, the default stored by `slopcop login --default` or `slopcop service use`, `https://slopcop.com` |
| Token | `$SLOPCOP_TOKEN`, the login stored for that service |
| Workspace (`check`, `enable`, `status`, `ignore`, `unignore`, `ignored`, `repo`) | `-w`/`--workspace`, `$SLOPCOP_WORKSPACE`, the one this machine maps the checkout's repository to, the one the login is bound to |

## Workspace and repositories

A login is bound to one workspace: the one active in the browser when the
login was approved. Its checks and ignores run in, and bill, that workspace,
unless this machine maps the repository to another of your workspaces.

```console
slopcop whoami                     # account, this login's workspace, its plan, the service URL
slopcop status                     # this checkout's repository, and whether the workspace checks it
slopcop enable                     # optional: add this checkout's repository now, and say which workspace
slopcop workspace list             # the account's workspaces; * marks this login's
slopcop workspace map "Acme team"  # check this checkout's repository there from now on, at a terminal
slopcop workspace unmap            # go back to the login's workspace for this repository
slopcop workspace mappings         # the repositories this machine maps, and to which workspace
slopcop workspace use "Acme team"  # move this login (not the browser) to another, by name or id
slopcop check -w "Acme team"       # check in, and bill, another workspace this once
slopcop repo list                  # the workspace's repositories: active, disabled, or over the limit
slopcop repo disable acme/widgets  # stop checking one (owner only)
slopcop repo enable acme/widgets   # turn it back on, within the workspace's active limit
```

`status`, `whoami`, `workspace list` and `repo list` take `--format human|json`,
as do `workspace use` and `repo enable|disable` for their answer. Repositories
are free; checks use the workspace's free checks for the month, then credits. A
disabled repository is not checked and keeps its reports, findings and settings.
`repo enable` and `repo disable` ask you to type the repository's `owner/name`
and refuse when stdin is not a terminal: an agent or CI job runs with your
login, and the service cannot tell it from you, so changing what the workspace
checks stays a step you take yourself.
`workspace use` moves every agent and script using this login, not only the
one that runs it, so it asks `[y/N]` and refuses when stdin is not a terminal.
To check in another workspace without moving anyone, `check`, `status`,
`ignore`, `unignore`, `ignored` and `repo` take `-w`/`--workspace <name|id>`
(or `$SLOPCOP_WORKSPACE`) for that one command; the login stays where it was. Give agents that share a login their
own `--workspace` instead of switching it.

`workspace map <name|id>` makes every checkout of the repository `origin`
names, on this machine, check in that workspace, so one login checks work
repositories in a team workspace and your own in your personal one. `slopcop
enable` maps the checkout to the workspace it adds the repository to. The
mapping is kept in `config.json`, by service and repository URL; it decides
which workspace pays, so `workspace map` and `workspace unmap` ask `[y/N]` and
refuse when stdin is not a terminal. `--workspace` and `$SLOPCOP_WORKSPACE`
still win for one command, and `slopcop status` prints the workspace a
checkout gets and a `Chosen by` line saying why. Pull request checks do not
follow the mapping: each workspace turns them on for a repository in the web
app, and `status` names your other workspaces that have the repository. A mapping to a workspace that
was archived, or that your account left, stops the check with
`workspace_not_found` instead of falling back to another workspace. The first
check of a repository with no mapping, when your account has more than one
workspace, notes once which workspace it runs in and any other of yours that
has the repository; the check still runs.

Names match without regard to case; when two workspaces share a name, or two
providers a repository name, pass the id the listing prints.

`slopcop enable` is optional. Run inside a checkout, it adds the repository the
`origin` remote names to the workspace that checks and bills it, runs its first
check, and maps this machine's checkouts of it to that workspace. It does not
ask: the workspace is the one `--workspace` names, else the one the checkout is
mapped to, else the login's, and it says which one on stderr along with how to
undo it:

```console
$ slopcop enable
slopcop: added Acme/widgets to "Acme team"; your agent's changes here are checked there from now on.
slopcop: wrong workspace? Run `slopcop workspace map <name>` here to check it in another (`slopcop workspace list` names them), then `slopcop repo disable Acme/widgets --workspace "Acme team"` to remove it from "Acme team".
```

An account with one workspace gets the first line alone, ending with the
`slopcop repo disable owner/name` that undoes it. Before it adds a repository another of your
workspaces already has, it says so, since both would then check and bill it.
A repository that is already active is left alone. One that someone disabled
is enabled again, but only at a terminal: an agent running with your login does
not undo that choice.

A check does not need `enable` first: a check of a repository the workspace has
never added adds it to the workspace the check runs in (the login's, or the one
`--workspace` names), which then pays for its checks, so an agent is not
stopped to ask. A check of one the workspace has disabled exits `4` with
`repository_disabled`, and one past the active limit with
`active_repository_limit_reached`; these stop before the local reviewers start,
so the report has no review. A check that adds a repository says `added owner/name to "Workspace"` on stderr, and `json` reports
`"repositoryAdded": true` and `addedToWorkspace`: the workspace, how it was
chosen, and how to check the repository in another one instead. `--add-repository` is no longer needed; it is still
accepted. Deleting repositories, archiving workspaces, members, credits and
repository settings stay in the web app. `enable` exits with its first check's
result; the other account commands exit `0` on success and `2` with the
service's message and remedy otherwise, as `ignore` does.

## Check

```console
slopcop                          # check the change in the current checkout
slopcop check --if-enabled       # automatic check: skip a repository disabled here
slopcop check --format json      # same, machine-readable
slopcop --root ../repo --base origin/main
```

The base commit defaults to the merge base with the branch upstream, then the
merge base with `origin/HEAD`, then `HEAD` when neither is known. When the
upstream merge base yields an empty patch, as on a branch whose commits are all
pushed, the CLI uses the merge base with `origin/HEAD` instead, so a check
right after a push still covers the branch. `--base REV` names the branch the
change started from and, like `git diff REV...HEAD`, uses the merge base of
`REV` and `HEAD`, so commits that landed on `REV` after the branch point are not
sent as reverse edits. A branch stacked on `REV` that has since merged a newer
`origin/HEAD`, or `REV`'s own upstream, moves the base to that line's merge base
once it holds `REV`'s, as after `REV` landed, so the merged-in commits are not
sent either. When the lines have diverged, no fetchable commit holds both, so
the base stays and the CLI warns that the patch carries the merged-in commits.
`--base-exact REV` diffs against `REV` itself. The
service must already have the base commit in its clone; a missing one comes back as the service's
`base_not_found` message and remedy. The CLI then asks origin for its default
branch (`git ls-remote`, no prompt, 10 second limit) and adds a line when the
clone explains the failure: origin's default branch was renamed and
`origin/HEAD` still names the old one, or the base is not on origin's default
branch.

Every run with a static half sends one check request, even when the working tree
matches the base and the patch is empty. The static result comes from the
service; local review runs separately. A report for an empty patch comes with a
note on stderr that suggests `--base`, and `json` adds `"emptyChange": true`
(`sarif`: `properties["slopcop.emptyChange"]`): a clean result then means
nothing changed was checked.

Flags:

| Flag | Meaning |
| --- | --- |
| `--mode both\|static\|ai` | both halves (the default where reviewers are configured, outside CI), static only (the default otherwise), or review only |
| `--static`, `--ai` | shortcuts for the corresponding mode |
| `--intent TEXT` | user request when no session transcript is found |
| `--review-timeout DURATION` | local review timeout, and the longest a check waits for another review or override in the same checkout (default `20m`) |
| `--root PATH` | repository to check (default `.`) |
| `--base REV` | branch or revision the change started from; the base is its merge base with `HEAD` |
| `--base-exact REV` | exact base commit or revision, without a merge base |
| `--format human\|json\|sarif` | report format (default `human`) |
| `--fail-on never\|finding\|note\|warning\|error` | exit-1 threshold for new static findings (default `finding`); a review finding exits `1` at every level but `never` |
| `--certainty definite\|all` | report and fail on only `definite` findings (default), or on `possible` and `heuristic` ones too |
| `--policy PATH` | repository-relative `.rqlp` file to evaluate (repeatable) |
| `--policy-id ID` | built-in policy ID (repeatable) |
| `--policy-pack PACK` | policy pack (repeatable) |
| `--policy-category CATEGORY` | policy category (repeatable) |
| `--no-policy-discovery` | skip `.slopcop/policies` rules |
| `--timeout DURATION` | how long to wait for the result (default `10m`) |
| `-w`, `--workspace NAME\|ID` | check in, and bill, this workspace of the account instead of the login's |
| `--if-enabled` | skip a repository the workspace has disabled or has past its active limit, exiting `0` without sending a check; one it has never added is added |
| `--verbose` | print the resolved request to stderr, and name each inconclusive check in `human` |

The hosted `slopcop.similarity` check runs by default, including when the
repository has `.slopcop/policies` files. It reports similar
existing functions for functions in the change, with candidate paths, lines,
and a probability. Similarity findings have `info` severity, are always shown
regardless of `--certainty`, and are advisory: they never affect the push gate
or the check's exit code. Check notices, including partial comparison notices,
appear as notes and do not make analysis incomplete. To select only
similarity, pass `--policy-id slopcop.similarity`.

This is the one hosted check that is not rule-based: the hosted service sends
function source to third-party providers. An embedding provider receives the
functions the change adds or edits and, to index the repository, the functions
in the rest of its source files, not only the ones the change touches. A model
receives each changed function with the existing functions closest to it.

A finding of the hosted `slopcop.dependencies.known-vulnerability` check is a
known-vulnerable dependency version the change brings in. Its location is the
lockfile entry, and the report adds the package, how it is installed and the
fix:

```text
  [error] slopcop.dependencies.known-vulnerability: lodash 4.17.20 has a known vulnerability
    Location: package-lock.json:12
    Certainty: definite
    ID: 6b1f0c0e-0000-4000-8000-000000000003
    Package: npm lodash@4.17.20, direct, from package.json
    Advisory: GHSA-29mw-wpgm-hmr9 (CVE-2020-28500) https://osv.dev/vulnerability/GHSA-29mw-wpgm-hmr9
    Fixed in: 4.17.21
    Fix: npm install lodash@4.17.21 --save-exact (run at the repository root)
```

A transitive dependency adds a `Via:` line, from the direct dependency down
to the package. `json` passes the same fields through as the finding's
`dependency` object, and `sarif` puts it in the result's
`slopcop.dependency` property. The client never reads lockfiles or asks an
advisory database itself, and drops a `dependency` object that is malformed,
oversized or unsafe to print, keeping the finding. `slopcop ignore` takes the
finding's id like any other.

### Modes and local review

`slopcop check` runs hosted static analysis and local LLM review concurrently
by default. Use `--mode static` or `--static` for only the hosted check; use
`--mode ai` or `--ai` for only local review. A single captured snapshot
includes committed, uncommitted, and untracked work, except ignored files.
Reviewers read the captured diff after the same large-data filter used for
the hosted patch. The full snapshot still identifies the tree approved for push.

Reviewers, in priority order, are GPT Terra (`terra`) and GPT Sol (`sol`) through
Codex ACP, DeepSeek Flash (`flash`) through dsh ACP, and Claude Haiku (`haiku`)
through Claude ACP. Each uses the newest model of its family at high effort.
Gemini (`gemini`) through Antigravity CLI is opt-in: init never picks it, because
Antigravity's web search cannot be refused, only detected. See
[Gemini through Antigravity](https://docs.slopcop.com/reference/configuration/#gemini-through-antigravity-opt-in).
Kimi K3 (`kimi`) through Kimi Code CLI is also opt-in and needs `KIMI_API_KEY`. See
[Kimi through Kimi Code](https://docs.slopcop.com/reference/configuration/#kimi-through-kimi-code-opt-in).
Grok (`grok`) through Grok Build is also opt-in and needs `XAI_API_KEY`. See
[Grok through Grok Build](https://docs.slopcop.com/reference/configuration/#grok-through-grok-build-opt-in).
`slopcop init` probes each family once when reviewers are not configured and
mode is not static, then saves the first two that work (or one) as reviewers
and the rest that work as `review.fallbacks`. Configured reviewers are left
alone; unset `review.reviewers` and run init to pick again.
The probe skips a family whose login or API key is plainly missing (Codex or
Claude Code never logged in here, `DEEPSEEK_API_KEY` unset), and prints one
line when none work; `slopcop init --verbose` says why each one failed.
`slopcop init --no-reviewers` skips probing. A reviewer that cannot start does
not stop the others. One that ends without a verdict runs once more in a fresh
session; if it still fails, the next unused fallback stands in for it,
preferring another provider. The review is clean when at least
`review.minCompleted` reviewers (default 2, at most the number of reviewers)
finished clean, and fails with exit `2` when fewer did. Findings from any
reviewer count. The minimum only tolerates a failure when more reviewers can
run than it requires, so configure fallbacks on another provider than the
reviewers: one outage or empty account then cannot fail every check. Set the
ordered list (1–4
entries; duplicates allowed) with `slopcop config set review.reviewers terra,sol`.
Set `review.fallbacks` (ordered, like `review.reviewers`), `review.timeout`
(default `20m`), `review.minCompleted` (1 up to the number of reviewers) or
`mode` with `slopcop config set`;
adapter commands are under `review.adapters.<codex|dsh|claude|copilot|antigravity|kimi|grok>.command`.
The prompt carries the captured diff. Reviewers read repository content,
which is untrusted. Claude and DeepSeek reviewers may only read and search
files; Slopcop refuses their requests to run commands or change files. Codex
reviewers run in Codex's read-only sandbox.
CI normally uses `--mode static`.

Review state and logs are under `<git-dir>/slopcop/`. `slopcop review show
[--format human|json|sarif]` prints the latest review again, rebuilding each
reviewer's reply from its log, without running a reviewer. A complete review is
reused when nothing relevant changed. A nonempty check with a clean review
and a completed static result with no new findings at or above
`gate.blockOn` (default `error`) records `refs/slopcop/reviewed/<tree>`.
Warnings and notes are advisory for push approval, though they still exit `1`
by default, and a pull request check fails on them: JSON `advisoryFindings`
counts them, and stderr says so where the push gate is installed. Set
`gate.blockOn` to `finding` to block what a pull request check fails on. The Claude Code gate checks HEAD's tree, so
after checking a dirty tree, commit exactly that state. For demonstrably wrong
complete review findings, `slopcop override <rv-id> --reason "..."` overrides
one finding, by the `rv-` id in `review.findings`, for every later review in the
clone: check again and the reused review leaves it out, later reviewers are
told not to raise it, and a finding that repeats it, even reworded near the
same lines, is listed under `review.overridden` instead of failing the check.
`--undo <rv-id>` and `--list` manage them. Without an id,
`slopcop override --reason "..."` records acceptance
when the static half has no findings at or above the push level
and completed, was skipped, or did not run. Never override real bugs or failed or incomplete reviews.

### What is sent

In modes with the static half, the CLI sends one git patch from the base commit
to the working tree. An empty change is sent as an empty patch, not filtered
out. The patch carries committed but unpushed work, unstaged edits, untracked
files, deletions, renames, mode changes, and binary content. Git-ignored files
stay out, except selected `.slopcop/policies` rules, which must travel because
the service evaluates them from the patched checkout.

By default, changed JSON data (`.json`, `.jsonl`, `.ndjson`, case-insensitive)
and Git-detected binary files are omitted when either the base or captured
version exceeds **500,000 bytes (0.5 MB)**. Source files and smaller JSON
configurations remain in scope. The filter runs before generating the upload
or local review diff, covers deletions and both sides of renames, and never
changes your files or index. Every report discloses omitted files: human output
names each path with its size and reason, JSON carries `omittedFiles`, and SARIF
carries `slopcop.omittedFiles`. Their contents were not analyzed; a passing
result covers the retained scope. If every change is omitted, the check is
`skipped`, sends nothing, and records no push approval.

Use repeatable `--include` paths or globs to opt particular data files back in:

```console
slopcop check --include config/settings.json --include 'models/**/*.json'
```

Patterns are relative to the repository root; quote globs so the shell does
not expand them. They only override the size filter and do not exclude other
changed source files. Service payload limits still apply to the resulting patch.

Changed files that `.bifrostignore` or `[check] exclude` keep out of every
check are left out of the upload and local review too, listed in
`omittedFiles` with reason `excluded`; `--include` does not bring them back.
A deleted file is sent without its old content, so removing a large folder
does not cost its size.

A check run by a Claude Code, Codex or OpenCode agent also sends which models wrote the
change, so the service can tie each finding to the model that wrote its file.
The CLI finds the agent's session from its environment and reads that
session's own record:

- **Claude Code:** `CLAUDE_CODE_SESSION_ID` names the transcript under
  `~/.claude/projects` (or `$CLAUDE_CONFIG_DIR/projects`), subagents included.
  Each successful Write, Edit, MultiEdit or NotebookEdit gives a model, an
  effort and a file.
- **Codex:** `CODEX_THREAD_ID` names the rollout under `~/.codex/sessions` (or
  `$CODEX_HOME/sessions`). Each completed file change gives the files, and its
  turn gives the model and effort. A move counts as writing its destination.
- **OpenCode:** `OPENCODE_SESSION_ID` names the session in OpenCode's
  database, `~/.local/share/opencode/opencode.db` (or `$OPENCODE_DB`),
  subagents included. Each completed edit, write or patch gives the files,
  and its step gives the model and its variant as the effort.

For static attribution, it reads only those fields, never the conversation, tool
input, or file contents from the session. What it sends is the harness, its
version, the session id as an opaque identifier, and each model and effort with
the repository-relative files it wrote. A check run outside an agent session
sends none of this. It is analytics only, asserted by the client, and never
decides a check. `--verbose` prints it.

The service owns the limits on what a check accepts. When a change is too
large or touches too many files, the service's `payload_too_large` response is
printed verbatim, and so is `repository_too_large` when the repository itself is
over the service's size limit; the CLI does not impose its own limits. The
service names no paths, so when it refuses a repository as too large, or answers
that its checkout is near the limit (`repositorySize.nearLimit`), the CLI lists
the base tree's largest paths itself, after what `.bifrostignore` and `[check]
exclude` already leave out, with a `[check] exclude` snippet to keep the
generated or data paths among them out of checks.

Every check with a static half also runs the hosted catalog. Rules under
`.slopcop/policies/**/*.rqlp` are added automatically unless
`--no-policy-discovery` is set.

### New and pre-existing findings

The static half fails only on findings the change introduced or touched:
the service
marks a finding new when it is in a file the change writes and its lines meet a
line the change added, or the line beside one it removed. Findings that were
already in the code are counted (`summary.existingFindings`, and `N already in
the code` in the human summary), never fail the check, and are not listed. A
change that introduces nothing exits `0`, however many findings the repository
already holds. `--show-existing` is deprecated and hidden: the service no longer
sends those findings, so against it the flag lists nothing (a service from
before that still gets them listed), and it stays so that scripts passing it
keep running.

### Ignore a false positive

Each finding a check reports carries an `id`. When a finding is a false
positive, ignore it:

```console
slopcop ignore 0f8fad5b-d9cb-469f-a165-70867728950e
```

That finding is not reported again for this repository. Ignoring covers that
one finding, its rule plus its place in the code, in its repository for your
workspace; the rule keeps reporting everything else. Ignoring a finding twice
is fine. `ignore` uses the same login and service URL as a check, exits `0`
with a one-line confirmation, and otherwise exits `2` with the service's
message and remedy; `finding_not_found` means the id is unknown or comes from a
check outside your workspace.

To undo a mistaken ignore, run `slopcop unignore <id>` with the id it was
ignored from, or the same finding's id from any later check of the repository.
Later checks of the repository report the finding again. Unignoring a finding
that is not ignored is fine; it exits and fails like `ignore`.

`slopcop ignored` lists the findings your workspace ignored in this repository,
newest first, 25 per page (`--page <n>`), with each one's id for `unignore`,
location, rule, and when and by whom it was ignored. `--format json` prints the
service's answer.

### Output and exit codes

The service answers structured data; the CLI renders it. `human` prints one
block per new finding, with an `ID:` line when the service names one, then a
summary line such as `1 new finding across 1 file(s); 72 already in the code |
Analysis complete`, and any omitted new findings or
incomplete checks. `json` prints `status`, `baseCommit`, `summary`
(`newFindings` and `existingFindings` count the split), `findings` (each with
`new` and its `id`), `incomplete`, `inconclusive`, `failure` when the analysis did not
complete, and `emptyChange` when the patch was empty. When the check
installed or updated the skill, `skillUpdated` lists each skill directory it
wrote: `dir`, `from` (a release, `unknown`, or absent for a fresh install),
`to`, `changed` and, for a skill with local edits, `backup`; the failed and
pending reports carry it too, and `sarif` puts it in
`properties["slopcop.skillUpdated"]`. `sarif` prints a SARIF 2.1.0 log whose results carry `baselineState`
`new` and the finding id in `properties["slopcop.findingId"]`. `--format` only
changes the rendering, never the request, so the service answers the same check
whichever is used.

| Exit | Meaning |
| --- | --- |
| `0` | Check completed; no new finding crossed the `--fail-on` level |
| `1` | A new static finding crossed `--fail-on`, or a reviewer reported a finding (unless `--fail-on never`); JSON `exitReason` says which |
| `2` | No new finding crossed `--fail-on`, but a check was incomplete; or the CLI or service failed |
| `3` | The check was still running when `--timeout` ran out |
| `4` | The service refused the check, and only a person can clear it |
| `130`, `143` | Interrupted before the check answered: `130` for Ctrl-C, `143` for SIGTERM |

`--fail-on` decides only the exit code: `status` is `findings` whenever the
change has new findings, so `--fail-on never` reports them and still exits `0`.
Otherwise only `clean` exits `0`: a status this CLI does not know exits `2`, so
a newer service can never pass a check by accident, and stderr names the
status. An
interrupted check prints no report. Interrupted before it was sent, nothing
reached the service; after, a service that received it keeps running it, and
running the same check again picks up its result.

A reviewer's finding fails the check by default, under every `--fail-on` level
but `never`. Review findings carry a priority (`P0` to `P3`), not a static
severity, so `note`, `warning` and `error` do not filter them. They fail by
default because a script, CI job or agent loop that reads only the exit code
would otherwise pass a change a reviewer flagged. The static counts in
`summary` (`findings`, `newFindings`) never include them, so a check the review
alone fails can show `summary.findings: 0` and `staticStatus: clean` and still
exit `1`. JSON `exitReason` names what failed it, and stderr says so in every
format, with the count and where the review findings are listed. Fix the real
ones and check again. For a wrong one, `slopcop override <rv-id> --reason "..."`
and check again: the reused review leaves it out and later reviews do not
report it. `slopcop override --reason "..."` without an id approves the tree
for push but does not change the exit code; `--fail-on never` keeps review
findings, and static ones, from exiting `1`.

An inconclusive check, one that ran but could not prove there is nothing
more to find, never changes the exit code or the status. Complete proof is
rare in static analysis, typically at a library boundary, so this is the norm
rather than a gap. Each report still says where they are, so a reviewer can
judge what a `clean` result is worth: `human` prints one `Inconclusive:` line
with the count and how many concern only files the change does not write, and
`--verbose` names each check and the files it concerns. `json` lists them in
`inconclusive`, each with `policyId`, `reason`, `detail`, `files` (when the
analyzer named every file the check concerns) and `elsewhere` (true when it
concerns only files the change does not write), beside the
`summary.inconclusive` count. `sarif` adds a `note` notification per check
with `slopcop.inconclusive` and `slopcop.elsewhere` properties and one
location per file. The service names at most 50; the counts cover the rest.
It does not yet say which functions or lines each one concerns. A check that timed out or
that Bifrost judged unreliable is incomplete and counts wherever it is.

The service applies `--fail-on` and sets the status; the exit code follows it.
The CLI always sends `--fail-on`, defaulting to `finding`: a check fails only
on findings the change introduced, so any of them, a note or an unrated one
included, is worth fixing before the change lands. The service's own default
for a request without `failOn` is Bifrost's `warning`; pass `--fail-on warning`
to match it.
New findings that cross `--fail-on` exit `1` even when some check did not
complete.

While the service answers `check_running`, the CLI waits the `Retry-After` it
names and asks again until `--timeout` runs out. It asks by the `checkId` the
answer carries (`GET /v1/check/{checkId}`), so the change is uploaded once; a
service that sends no `checkId`, or no longer knows it, gets the same request
again, which attaches to the same running check. If it is still running then, the CLI exits `3`, prints the
service's message and remedy on stderr, and in `json` prints
`{"status": "pending", "retryAfter": 5, "message": …, "remedy": …}` on stdout
(`sarif` prints a run with `slopcop.status` `pending`). Running the same check
again picks up the same analysis.

Service failures print the closed contract exactly: message, remedy, and any
install URL. The codes, messages, and remedies are defined by the service in
`server/src/check.rs` of `BrokkAi/bifrost-service`; the CLI does not
rewrite them. The remedies name no client, so the CLI adds its own command
after some: `slopcop login` when the remedy is to sign in, the listing to
choose from after `workspace_not_found` or `workspace_repository_not_found`,
and `slopcop whoami` after `repository_disabled`, since the workspace that
disabled it is the one this login is bound to, and `--workspace` after
`owner_required`. A failed check exits `4` when only a person can clear the
refusal (`auth_required`, `auth_expired`, `account_required`,
`repo_not_indexed`, `app_not_installed`, `app_no_repo_access`,
`gitlab_not_connected`, `no_access`, `insufficient_credits`,
`repository_disabled`, `repository_add_unconfirmed`, `workspace_repository_limit_reached`,
`active_repository_limit_reached`, `owner_required`, and `browser_required`),
so a script can stop instead of retrying, and `2` otherwise. The CLI refuses a
check of a repository the workspace has disabled locally, as
`repository_disabled`, and one past the active limit as
`active_repository_limit_reached`, before sending anything or starting the
local reviewers. Stderr starts with the code, as in `slopcop:
repository_disabled: …`. `human` prints nothing on
stdout; `json` prints `{"status": "failed", "code": …, "message": …,
"remedy": …, "retryable": …}`, plus `installUrl` or `topUpUrl` when the
service sent one; `sarif` prints a run with `slopcop.status` `failed`, whose
invocation is unsuccessful and carries an `error` notification whose
descriptor id is the code.

## API

The CLI calls `POST <service>/v1/check` with a bearer token:

```json
{
  "repoUrl": "https://github.com/owner/name",
  "baseCommit": "0123456789abcdef0123456789abcdef01234567",
  "change": "<base64 git patch>",
  "selection": {"files": [".slopcop/policies/rule.rqlp"]},
  "failOn": "finding",
  "addRepository": true
}
```

`addRepository` is sent when the workspace has never added the repository, so
the check adds it. The service rejects any other field.
With `--workspace`, every request, the check's, the ignore's and the account
routes', carries the workspace's id in the `SlopCop-Workspace` header; a name
is first looked up with `GET <service>/v1/whoami`. The response carries `status`,
`baseCommit`, `applied`, `summary`, `findings`, `incomplete`, and `failure`
when the analysis did not complete. Against a service from before findings
were marked `new`, the CLI treats every finding as new, as that service gated
on all of them. Non-2xx responses carry `code`, `message`,
`remedy`, `retryable`, and an optional `installUrl`.

Each finding in the response carries an `id`; a service from before findings
could be ignored omits it.

`slopcop ignore <id>` calls `POST <service>/v1/ignore` with the same bearer
token and `{"findingId": "<id>"}`, and gets back `{"findingId": "<id>",
"status": "ignored"}`. Failures use the same closed contract, including
`finding_not_found`. `slopcop unignore <id>` calls `POST <service>/v1/unignore`
the same way and gets back `{"findingId": "<id>", "status": "unignored"}`, also
when the finding was not ignored. `slopcop ignored` calls `GET
<service>/v1/ignores?repoUrl=<url>&page=<n>` (`page` zero-based) and gets back
`repository`, `total`, `page`, `pageSize` and `ignores`, newest first; see the
[API reference](docs/src/content/docs/reference/api.md).

The account commands use the same bearer token. `GET <service>/v1/whoami`
answers `account`, `workspace` (`id`, `name`, `kind`, `role`, `current`),
`plan` (`pro` or `free`) and `workspaces`. `POST <service>/v1/workspace` with
`{"workspaceId"}` rebinds the login. `GET <service>/v1/repositories` answers
`repositories` (`id`, `provider`, `fullName`, `enabled`, `active`) with
`activeRepositoryLimit`, `activeRepositories` and `canManageRepositories`, and
`POST <service>/v1/repositories/state` with `{"repositoryId", "enabled"}`
answers `{repositoryId, fullName, enabled, active}`. Their failure codes add
`account_required`, `workspace_not_found`, `workspace_repository_not_found`,
`owner_required` and `browser_required` (a private Bitbucket or GitLab
repository, which only the web app can change) to the check's.

`slopcop login` uses the device flow: `POST /api/device` returns a
`deviceCode`, `userCode`, `verificationUri`, `expiresIn`, and `interval`, and
`POST /api/token` returns `202` while pending or `200` with a `token` once
approved. The failure codes are `slow_down`, which raises the polling interval,
`expired_token`, `access_denied`, and `device_flow_disabled`. A failure the
service marks `retryable`, such as `rate_limited` or `service_unavailable`,
keeps the code valid, and so do a dropped connection and a proxy's `429`,
`502`, `503` or `504` without a code: login waits the `Retry-After`, or the
interval if that is longer, and polls again until the code expires.

## Development

```console
make test        # go test ./...
make vet         # go vet ./...
make build       # bin/slopcop-<goos>-<goarch>
make cross-check # linux, darwin, and windows on amd64 and arm64
make npm-test    # node --test npm/test
make wheels      # pypi-dist/, one wheel per release target
```

Releases are tag-driven. Pushing a `v*.*.*` tag runs GoReleaser, which builds
linux, darwin, and windows for amd64 and arm64, packages `tar.gz` archives
(`zip` on Windows) with `checksums.txt`, and publishes the GitHub release.
`.goreleaser.yaml` is the single source of truth; CI runs `goreleaser check`
and a snapshot build on every change, and `make release-check` runs the same
locally when GoReleaser is installed. The same tag push then packs
`@brokkai/slopcop` from those release archives and publishes it to npm with
[trusted publishing](docs/src/content/docs/contribute/npm-publishing.md), so the package needs no npm
token.

The same tag also packs a wheel per release target from the binaries GoReleaser
just built, installs the wheel for every platform GitHub can run, and
publishes `brokk-slopcop` to PyPI with
[trusted publishing](docs/src/content/docs/contribute/pypi-publishing.md), so that package needs no PyPI
token either.

Layout:

| Path | Purpose |
| --- | --- |
| `cmd/slopcop` | process entry point |
| `internal/cli` | cobra commands, selection, exit codes |
| `internal/gitx` | remote canonicalization, base resolution, patch building |
| `internal/service` | `/v1/check` client and the wire contract |
| `internal/report` | human, JSON, and SARIF rendering from the structured response |
| `internal/config` | stored service URL and token |
| `internal/hooks` | Claude Code and Codex hook installation and removal |
| `npm` | launcher, packer, and tests of the `@brokkai/slopcop` package |
| `packaging/pypi` | wheel packer and launcher of the `brokk-slopcop` package |
| `skills/slopcop` | the agent skill the binary embeds and installs |
| `docs` | the documentation site at <https://docs.slopcop.com> (Astro Starlight), including the npm and PyPI release flows |
| `.goreleaser.yaml` | tag release build, archives, and checksums |
