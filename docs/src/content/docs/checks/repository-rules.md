---
title: Repository rules
description: "Add your own Bifrost policies under .slopcop/policies and every check evaluates them alongside the hosted catalog. Naming rules, replacing a built-in, selecting rules by hand, and testing a rule while you write it."
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
    sql/
      no-raw-query.rqlp          # :id "acme.sql.no-raw-query"
    http/
      no-network-in-handlers.rqlp  # :id "acme.http.no-network-in-handlers"
```

## Name a rule

A rule's id is what a finding reports as its `policyId`, what
[`[[check.exclude_rules]]`](/checks/false-positives/#exclude-paths-from-checks)
matches, and what decides whether the rule replaces a built-in. We suggest:

- **Id:** `<owner>.<area>.<rule>`, in lowercase words joined by hyphens, where
  `<owner>` is your organization or team, for example `acme.sql.no-raw-query`.
  Keep the id once the rule is in use: exclusions that name it stop matching
  when it changes.
- **File:** the id after the owner, as a path under `.slopcop/policies`:
  `acme.sql.no-raw-query` lives in `.slopcop/policies/sql/no-raw-query.rqlp`.
  One rule per file.
- **Reserved prefixes:** do not start an id with `bifrost.` or `slopcop.`. The
  hosted catalog uses them, and a rule with a catalog id replaces that built-in
  (see below), including one a later catalog adds.

```text
(policy :schema-version 1
        :id "acme.sql.no-raw-query"
        :name "No raw SQL queries"
        ...)
```

## Replace a built-in

A rule whose `:id` is a built-in's id runs in place of that built-in. To tune a
catalog rule for your codebase, copy it into `.slopcop/policies` under its id
and edit the copy: checks then run your version and not the catalog's.

This holds while the catalog runs by default. A check that also names that
built-in with `--policy-id` has two policies with one id, and fails with
`policy_invalid`.

## Select rules by hand

```console
slopcop check --policy rules/no-raw-sql.rqlp --policy rules/handlers.rqlp
slopcop check --no-policy-discovery --policy rules/no-raw-sql.rqlp
```

`--policy PATH` names a repository-relative `.rqlp` file to evaluate, and
repeats. It adds to the discovered rules unless `--no-policy-discovery` drops
them.

Repository rules always run with the hosted catalog. By default that is the
whole default catalog; `--policy-id ID`, `--policy-pack PACK` and
`--policy-category CATEGORY` (each repeatable) run only the built-ins they name
instead.

## Test a rule while you write it

A check reports a finding only on lines the change wrote, so test a rule on
code that is part of the change:

1. Write the rule under `.slopcop/policies`.
2. Add two small files in the language the rule targets: one the rule should
   flag, and one that only looks similar and should not. Untracked files are
   part of the change, so there is nothing to commit.
3. Check with only that rule from the repository, and the static check alone:

   ```console
   slopcop check --static --no-policy-discovery \
     --policy .slopcop/policies/sql/no-raw-query.rqlp --format json
   ```

4. In the JSON, the rule's findings are the `findings` whose `policyId` is your
   rule's id. The positive file should have one at the line you expect; the
   look-alike should have none. If the rule appears in `incomplete` as
   `unreliable` instead, the service could not load it: check the file against
   the policy format.
5. Delete the two test files before you commit. Catalog rules run on them too,
   and the next check would report their findings as new.

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
