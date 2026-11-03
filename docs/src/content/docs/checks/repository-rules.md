---
title: Repository rules
description: "Add your own Bifrost policies under .slopcop/policies and every check evaluates them alongside the hosted catalog. Selecting rules with --policy and skipping discovery."
sidebar:
  order: 6
---

Every check runs the hosted catalog. A repository can add its own rules: Bifrost
policy files (`.rqlp`) that travel with the change and are evaluated from the
patched checkout, so a new rule is picked up by the next check with no extra
setup.

## Where rules live

Rules under `.slopcop/policies/**/*.rqlp` are added automatically unless
`--no-policy-discovery` is set. They are the one exception to the git-ignore
rule: a selected policy file is sent even when git ignores it, because the
service evaluates it from the patched checkout.

```text
.slopcop/
  policies/
    no-raw-sql.rqlp
    http/
      no-network-in-handlers.rqlp
```

## Select rules by hand

```console
slopcop --policy rules/no-raw-sql.rqlp --policy rules/handlers.rqlp
slopcop --no-policy-discovery --policy rules/no-raw-sql.rqlp
```

`--policy PATH` names a repository-relative `.rqlp` file to evaluate, and
repeats. It adds to the discovered rules unless `--no-policy-discovery` drops
them. The hosted catalog always runs.

## Writing a rule

A rule is written in Bifrost's Rune Query Language. The
[Bifrost documentation](https://bifrost.brokk.ai/static-analysis-policies/)
covers the policy format, the [rule language](https://bifrost.brokk.ai/rune-query-language/),
and a [walkthrough of building a rule](https://bifrost.brokk.ai/build-static-analysis-rule/)
from a fix your team keeps making. A rule has a stable id, a severity, and a
match or data-flow body; its findings come back in a check like the catalog's,
with the same certainty and the same new-versus-existing split.

A policy file the service cannot load makes that rule `unreliable` in the
report's `incomplete` list rather than silently clean, so a typo in a rule shows
up on the next check.

## Rules written for you

Teams that would rather not write rules can have them written: tell us the
rules your team keeps enforcing by hand, and we write and test each one against
code it should catch and code that only looks similar, then set it up in your
CI. See [custom static analysis](https://slopcop.com/custom-static-analysis) on
slopcop.com.
