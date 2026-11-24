---
title: Configuration
description: "Where slopcop keeps its credential and state, how the service URL, token, and workspace are resolved, and the files it writes in your home directory."
sidebar:
  order: 2
---

The CLI stores settings in the user config. `slopcop config get|set|unset`
manages `mode`, `review.reviewers`, `review.fallbacks`, `review.timeout`, and
`review.minCompleted`. Adapter commands are
edited in the config file.

## Precedence

| Value | Order |
| --- | --- |
| Service URL | `--service`, `SLOPCOP_SERVICE`, the default stored by `slopcop login --default` or `slopcop service use`, `https://slopcop.com` |
| Token | `SLOPCOP_TOKEN`, the login stored for that service |
| Workspace (`check`, `enable`, `status`, `ignore`, `unignore`, `ignored`, `dictionary`, `repo`) | `-w`/`--workspace`, `SLOPCOP_WORKSPACE`, the one this machine maps the checkout's repository to ([A workspace per repository](/account/workspaces/#a-workspace-per-repository)), the one the login is bound to |
| Base commit (`check`) | `--base-exact`, `--base`, merge base with the upstream, merge base with `origin/HEAD`, `HEAD` |
| Policy selection (`check`) | `--policy` files, plus `.slopcop/policies/**/*.rqlp` unless `--no-policy-discovery` |

## Files the CLI writes

All under the user config directory: `~/.config/slopcop/` on Linux
(`$XDG_CONFIG_HOME/slopcop/` when set), `~/Library/Application
Support/slopcop/` on macOS, `%AppData%\slopcop\` on Windows.

| File | Purpose |
| --- | --- |
| `config.json` | default service URL, one login per service, the workspace each mapped repository checks in (`repositoryWorkspaces`), mode (unset: `both` where reviewers are configured and not in CI, otherwise `static`), and review settings, written owner-only; logout forgets one service's login |
| `workspace-notices.json` | the repositories the once-only note about an unmapped checkout was considered for |
| `update-notice` | the time of the last [upgrade notice](/get-started/install/#upgrade-notices), so it prints at most once a day |
| `skill-backups/<harness>-<time>/` | a copy of a skill with local edits that an update replaced; see [The agent skill](/agents/skill/) |

In each agent's skills directory, `slopcop/.slopcop-skill.json` records the
version that wrote the skill and what each file held.

## Files the CLI edits

`slopcop init` edits the user-level settings file for each installed harness:
Claude Code's `settings.json`, Codex's `hooks.json`, and Grok's
`hooks/slopcop.json`. Each gets SlopCop's static Stop hook and push gate without
disturbing other entries. Init also installs both skills in the user-level
locations described above. An existing per-repository install is removed when
init runs in that checkout, and migrates itself the first time its old hook
runs the upgraded CLI elsewhere.
`slopcop init --remove-hooks` removes SlopCop's user-level and legacy
repository hooks and nothing else. See [The Stop hook](/agents/stop-hook/).

## Files in the repository

| Path | Purpose |
| --- | --- |
| `.slopcop/policies/**/*.rqlp` | [repository rules](/checks/repository-rules/), evaluated by every check |

The repository's URL for the static half comes from the `origin` remote.
The review needs no supported origin.

## Review settings and state

The config's `mode` is `both`, `static`, or `ai`. `review.reviewers` is an
ordered list of 1–4 family names; duplicates are allowed. Set it with
`slopcop config set review.reviewers terra,sol`, or inspect and remove it with
`get` and `unset`. `review.timeout` defaults to `20m`.

A reviewer that cannot start does not stop the others, and one that ends
without a verdict is retried once in a fresh session (not after a timeout, a
missing login or exhausted credits). `review.fallbacks` is an ordered list of
up to 4 families that stand in for a reviewer that still fails, preferring one
on another provider. `review.minCompleted` (default 2, at most the number of
reviewers) is how many reviewers, primaries or fallbacks, must finish for a
clean review. It tolerates a failure only when more reviewers can run than it
requires: with `terra,sol`, no fallbacks and the default, one failure fails the
review. Mix providers (Codex for `terra` and `sol`, Claude for `haiku`, `sonnet`
and `opus`, dsh for `flash`) between reviewers and fallbacks, so one outage or empty account does
not fail every check. Adapter command overrides
are keyed by adapter at `review.adapters.<codex|dsh|claude|copilot|antigravity|kimi|grok|opencode|openrouter>.command`. For
example, keep a DeepSeek key in a file with:

```json
"dsh": {"command": ["npx", "--yes", "@brokkai/dsh-acp@0.6.0", "--api-key-file", "/path/to/deepseek_api_key"]}
```

Otherwise dsh reads `DEEPSEEK_API_KEY`.

When `review.reviewers` is unset, `slopcop init` picks reviewers by sending
each family a short prompt. It tries different adapters at the same time and
prints each result as it arrives. It skips flash without a dsh command override
when `DEEPSEEK_API_KEY` is not set.

The Claude families are `haiku`, `sonnet` and `opus`. Each reviews with the
newest Claude model of its name at high effort, through the same Claude ACP
adapter and with the same restrictions: it may only read and search files.
Init tries them in that order, so a machine with only Claude Code gets
`haiku,sonnet` as reviewers and `opus` as a fallback. To review with the
stronger models, name them yourself:

```bash
slopcop config set review.reviewers sonnet,opus
```

They draw more on your Claude plan or API key than `haiku` does.

`review.provider copilot` runs `terra`, `sol`, `haiku`, `sonnet` and `opus` through GitHub
Copilot CLI instead of Codex and Claude, on your Copilot subscription; `flash`
keeps dsh, since Copilot has no DeepSeek model. When no reviewer of its own
works and Copilot may be signed in, `slopcop init` tries them through Copilot
and sets the provider itself. See
[GitHub Copilot CLI](/agents/copilot-cli/#copilot-as-a-reviewer).

### Gemini through Antigravity (opt-in)

The `gemini` family reviews with the newest Gemini Flash or Pro model your
account offers, at high effort, through Antigravity CLI's ACP server
(`agy acp`). `slopcop init` never picks it. Install Antigravity CLI, sign in
with `agy`, then name it yourself:

```bash
slopcop config set review.reviewers terra,gemini
```

It is opt-in because Antigravity's web search and subagents run without asking
the client, so SlopCop cannot refuse them. If a reviewer calls either one, or
any tool SlopCop does not recognize, SlopCop stops it at once, fails it without
a retry, and says so. **The call has already started by then, so a repository
that tricks the reviewer into a web search could get text from the change into
that query.** Leave `gemini` off for code that must not risk this.

Each Gemini reviewer runs with these limits:

- It gets a temporary copy of the reviewed tree, in a folder below an empty
  workspace, so Antigravity does not load the repository's hooks, MCP servers,
  rules or skills. The copy has no `.git` and no symlinks or submodules, and
  leaves out files over 1 MiB. A tree over 1 GiB fails the reviewer, and so
  does a change to a file the copy leaves out, because the reviewer could not
  read the code around it.
- It uses its own Antigravity config directory, so your personal hooks and MCP
  servers do not load and reviews stay out of your Antigravity history. Your
  login still applies. Telemetry is off.
- It may use only `read_file` inside that workspace. SlopCop refuses every
  permission request, including commands, edits and reads outside the
  workspace. A refusal ends Antigravity's turn, so that attempt fails and is
  retried once.
- It does not review a change whose diff is over 256 KiB, because it has no git
  history to read the rest. The reviewer fails instead of approving what it has
  not seen, so give it a fallback on another provider.

The adapter runs the installed `agy`, not a pinned package; it was checked
against version 1.5.14. A command override that includes
`--dangerously-skip-permissions` is rejected.

### Kimi through Kimi Code (opt-in)

The `kimi` family reviews with Moonshot's Kimi K3 (`kimi-k3`) at high effort,
through the ACP server of Kimi Code CLI, pinned to version 2.7.2. `slopcop init`
never picks it. It uses a Kimi platform API key and does not use a `kimi login`
session. Enter the key without saving it to your shell history, then name the
reviewer:

```bash
read -rs KIMI_API_KEY && export KIMI_API_KEY
```

```bash
slopcop config set review.reviewers terra,kimi
```

Keep `KIMI_API_KEY` set in the shell that runs `slopcop`. For a key from another
Kimi region, set `KIMI_BASE_URL` to that region's API URL. The default is
`https://api.moonshot.ai/v1`.

It is opt-in because the change and the files the reviewer reads go to Moonshot,
and because Kimi Code has no operating-system sandbox. **Its limits come from
Kimi Code honoring the configuration SlopCop generates.** Leave `kimi` off for
code that must not rely on that. These limits restrict what the reviewer can
do, not what it says: text in a repository can still sway its reply, as with
any LLM reviewer, so pair it with a reviewer from another provider.

Each Kimi reviewer runs with these limits:

- It gets the same temporary copy of the reviewed tree as a Gemini reviewer, so
  Kimi Code does not load the repository's MCP servers, rules, skills or agents.
  A change it cannot read fails the reviewer, as does a diff over 256 KiB or an
  approval given after a read of a changed text file failed, so give it a
  fallback on another provider.
- It has its own empty Kimi Code home and user home, so your personal config,
  hooks, MCP servers, skills and agents do not load, and reviews stay out of
  your Kimi Code history. Telemetry and update checks are off, and other `KIMI_`
  environment variables are not passed to it.
- Its only tool is `Read`, and a deny rule refuses any path outside the temporary
  workspace. SlopCop refuses every permission request.
- If any other tool starts, SlopCop stops the reviewer at once, fails it without
  a retry, and says so. **That tool has already started by then.**

These limits were checked against Kimi Code 2.7.2 on macOS, Windows and Linux.
A command override with `--yolo`,
`--auto`, `--auto-approve`, `--add-dir`, `--skills-dir`, `--agent` or
`--agent-file` is rejected.

### Grok through Grok Build (opt-in)

Checked live against Grok Build 1.0.92 on macOS on Apple silicon. Its Linux
sandbox has not been run with this reviewer.

The `grok` family reviews with the newest plain versioned Grok model your
account offers, such as `grok-4.7`, at high effort, through Grok Build's ACP
server (`grok agent stdio`). `slopcop init` never picks it. It needs an xAI API
key from [console.x.ai](https://console.x.ai) and does not use a `grok login`
browser session. Enter the key without saving it to your shell history, then
name the reviewer:

```bash
read -rs XAI_API_KEY && export XAI_API_KEY
```

```bash
slopcop config set review.reviewers terra,grok
```

Keep `XAI_API_KEY` set in the shell that runs `slopcop`.

It is opt-in because SlopCop cannot confirm everything the Grok CLI does from
outside it, including what the CLI itself sends over the network. Telemetry,
trace upload, session relay, web search, subagents and memory are turned off
through settings the CLI documents. **Grok's sandbox does not block network
access on macOS or for the CLI's own requests**, so these settings and the tool
limits below are what keep repository text from leaving through another route.
Leave `grok` off for code that must not risk this.

Each Grok reviewer runs with these limits:

- It gets the same temporary copy of the reviewed tree as a Gemini reviewer,
  with the same limits on size, symlinks and submodules, so Grok does not load
  the repository's hooks, MCP servers, rules or skills.
- It runs with an empty temporary home and Grok home. Your Grok, Claude Code and
  Cursor hooks, MCP servers, skills and history are not loaded, and the review
  is not saved to your Grok history.
- It runs in a custom sandbox profile based on Grok's `strict` profile that also
  denies your real home directory. Grok refuses to start if a custom profile
  cannot be applied. On Linux this needs `bubblewrap`. It does not run on
  Windows, or when `TMPDIR` is inside your home directory.
- It may use only `read_file`. SlopCop serves each read itself and refuses any
  path outside the workspace before reading it. It also refuses every permission
  request. If the reviewer calls any other tool, names a path outside the
  workspace, or finishes a read SlopCop did not serve, SlopCop stops it at once
  and fails it without a retry.
- It does not review a change whose diff is over 256 KiB, or a change to a file
  the copy leaves out. It fails instead of approving what it has not seen, so
  give it a fallback on another provider.

The adapter runs the pinned `@xai-official/grok@1.0.92` package with
auto-update and npm install scripts off. Because each reviewer has its own Grok
home, the package unpacks its binary there for each run, which takes about
190 MB of temporary disk until the reviewer finishes. A command override may
change how the CLI is started, but one that sets its permission mode, sandbox,
trust, plugins, agent profile, working directory or API endpoints is rejected.
An override that uses npm must have the form `npx --yes <package>@<version>`,
so that npm starts outside the reviewer's temporary directory. Any other npm
form is rejected.

### Ollama through OpenCode (opt-in)

The `ollama` family reviews on a model you installed in Ollama, through
OpenCode's ACP server. `slopcop init` never picks it. It needs Ollama 0.40.16 or
newer, Node for the pinned `opencode-ai` package, and a model that supports tool
calling:

```bash
ollama pull <model>
```

```bash
OLLAMA_CONTEXT_LENGTH=64000 ollama serve
```

```bash
slopcop config set review.ollamaModel <model>
```

```bash
slopcop config set review.reviewers terra,ollama
```

SlopCop never pulls a model. If you run the Ollama desktop app, set the context
length in its settings instead of running `ollama serve`. To raise it for one
model only, create a variant from a Modelfile with `PARAMETER num_ctx 64000` and
set `review.ollamaModel` to the variant's name.

**Where your code goes.** With `OLLAMA_HOST` unset or pointing at loopback, the
prompt and the files the reviewer reads stay on this machine. **If `OLLAMA_HOST`
names another machine, your code is sent to that machine**, and each reviewer's
result carries a warning saying so. SlopCop refuses Ollama cloud models and
`ollama.com` as a host, because they run on someone else's servers. The first
run downloads OpenCode from your npm registry, as the other adapters do.

**How much to trust it.** A small local model misses more than a hosted one, and
its `No findings.` is weaker evidence. Pair it with another reviewer rather than
relying on it alone.

Each Ollama reviewer runs with these limits:

- It gets a temporary copy of the reviewed tree, with the same exclusions and
  size limits as a [Gemini reviewer](#gemini-through-antigravity-opt-in), in a
  folder below an empty workspace. OpenCode does not load the repository's
  config, plugins, tools, MCP servers or skills.
- It uses a private home and a minimal environment, so your OpenCode settings,
  logins, sessions and API keys are not available to it. The copy and the home
  are deleted when it finishes.
- OpenCode offers the model only `read`, `glob` and `grep`, inside that
  workspace. If a tool of any other kind reports that it ran, SlopCop stops the
  reviewer and fails it without a retry.

These limits come from OpenCode's own permission settings, not an operating
system sandbox. They were checked against OpenCode 1.18.26, the pinned version, on macOS.
A command override must be `npx --yes <package>@<version>` or the path of an
OpenCode binary, so that npm starts outside the reviewer's temporary directory.
A repository's `AGENTS.md` still reaches the model as text when it reads a file
beside it, so a hostile repository can try to talk a weak model into approving.

The reviewer fails instead of approving what it may not have seen:

- when the prompt does not fit the model's context, or leaves too little room
  for a full reply. Ollama can default to as little as 4096 tokens, and SlopCop
  needs at least 16384.
- when the reply reaches its token limit, or OpenCode reports no token usage.
- when the diff is over 256 KiB, or a changed file is missing from its copy.
- when the reply is neither exactly `No findings.` nor findings tagged `[P0]`
  to `[P3]`. That case is retried once.

### OpenRouter (opt-in)

The `openrouter` family reviews with one model you choose from
[OpenRouter's catalog](https://openrouter.ai/models). `slopcop init` never picks
it. It needs an API key from [openrouter.ai/settings/keys](https://openrouter.ai/settings/keys).
Enter the key without saving it to your shell history, then name the model and
the reviewer:

```bash
read -rs OPENROUTER_API_KEY && export OPENROUTER_API_KEY
```

```bash
slopcop config set review.models.openrouter vendor/model
slopcop config set review.reviewers terra,openrouter
```

Keep `OPENROUTER_API_KEY` set in the shell that runs `slopcop`.

The model id is `vendor/model`, optionally with `:free`, `:nitro`, `:floor` or
`:exacto`. It must support tool calling. SlopCop refuses ids that do not name
one fixed model or that add capabilities: `openrouter/auto` and other routers,
`~...-latest` aliases, presets, `:online` and `:batch`. Pick a model from a
different vendor than your other reviewers, or the reviews are not independent.
The model runs at its default effort.

It is opt-in because your code goes through a third party, OpenRouter, on its
way to the model's provider. SlopCop does not use a general coding agent here.
It runs its own small agent, which has only three tools: read a file, list
files, and search files. Each reviewer runs with these limits:

- The tools answer from the reviewed git tree. They never open a path on disk,
  so symlinks, `..` and files outside the snapshot cannot be read.
- It has no shell, no file writes and no web access. It keeps no session
  history, and it loads no hooks, plugins, MCP servers, rules or other
  configuration from the repository or your home directory.
- It sends requests only to `https://openrouter.ai/api/v1`, follows no
  redirects, and keeps the API key out of its child processes, prompts and logs.
- Requests set `provider.data_collection` to `deny`, so OpenRouter routes only to
  endpoints that do not retain or train on prompts. If the model has no such
  endpoint, the reviewer fails. These policies are OpenRouter's and each
  provider's, and SlopCop cannot verify them.
- Requests turn OpenRouter's web plugin off. An account setting called "Prevent
  overrides" can force it back on. If a response shows a web search or another
  server-side tool, SlopCop stops the reviewer, fails it without a retry, and
  says so. **The search has already run by then, so a repository that tricks the
  model could get text from the change into that query.** Leave the web plugin
  off in your [OpenRouter settings](https://openrouter.ai/settings/plugins).
- A review is limited to 40 model requests, 8 MiB of tool output, and 32 MiB
  sent to the model in all, since each request resends the conversation so far.
  Reaching any limit fails the reviewer without a retry. A repository can still make a
  review cost more by prompting the model to read a lot, so use a key with a
  [credit limit](https://openrouter.ai/settings/keys).
- It does not review a change whose diff is over 256 KiB, or that touches a
  file over 1 MiB, a binary file such as an image, or a file that is not valid
  UTF-8. A reply cut off at the model's output limit or stopped by a content
  filter also fails. The reviewer fails instead of approving what it has not
  seen, so give it a fallback on another provider.

The key is read from your environment, so other reviewer adapters started by
the same check inherit it too. Like those adapters, it sends requests through
the proxy that `HTTPS_PROXY` or `ALL_PROXY` names, so a proxy you configure,
including one that inspects TLS, sees the review and the key.

Under `<git-dir>/slopcop/`, the CLI keeps `last.json`, `rounds.json`,
`overrides.jsonl`, and `review-*/` prompt and reviewer logs. Reviewer probe
logs are kept under `probes/` beside the user config because init is no longer
repository-specific. When a reviewer or probe is done, the CLI asks its adapter
to delete the session, so reviews
do not pile up in your agent's history: Codex archives the thread (`codex
unarchive <id>` restores it), Claude deletes it and Copilot closes it. Cleanup is skipped when an
adapter does not support it, and a failed cleanup never affects the review. Checks that
approve the tree (see [Exit codes](/checks/exit-codes/#which-results-approve-the-tree))
and valid overrides write `refs/slopcop/reviewed/<tree>`.

## Push approval level

Set `gate.blockOn` by editing the user config file. It defaults to `error`;
values are `never`, `finding`, `note`, `warning`, and `error`. New findings
at or above that level block push approval. Unknown severities count as
errors. This setting has no check flag and cannot be changed through
`slopcop config`.

A [pull request check](/checks/pull-requests/) does not read this setting: it
fails on any new finding. Under the default, a new note or warning is approved
for push here and fails there; set `finding` to block the same findings. See
[The push gate and pull request checks](/checks/exit-codes/#the-push-gate-and-pull-request-checks).

## Another deployment

```console
slopcop --service https://staging.slopcop.com login
slopcop --service https://staging.slopcop.com
```

Each service keeps its own login, and a login is only sent to the service that
issued it. `SLOPCOP_SERVICE` in the environment saves repeating the flag;
`slopcop service use <url>` makes a deployment the default. See
[Sign in](/account/sign-in/#the-service-url).
