---
title: What a check finds
description: "The kinds of findings a SlopCop check reports, how severity and certainty differ, which languages are covered, and what a check leaves out."
sidebar:
  order: 4
---

Every check runs the hosted policy catalog over the files the change writes,
plus any [repository rules](/checks/repository-rules/) the repository declares.
The catalog is a pinned set of [Bifrost](https://bifrost.brokk.ai) policies:
structural and data-flow rules that run on the source as written, with no build
and no language model. The service pins the catalog version, so a check run
twice on the same code gives the same result.

## Kinds of findings

Policy ids name their pack, so a finding's id tells you what kind it is.

| Pack | What it reports | Examples |
| --- | --- | --- |
| `bifrost.security.*` | A value from a request or other untrusted source reaches a sensitive operation without passing through a sanitizer. The finding names the path, across files. | A servlet parameter reaching the SQL text of `Statement.executeQuery` (`bifrost.security.java.servlet-parameter-to-jdbc`); code, LDAP, XML, XPath, and log-forging injection; a request value written to the application's own store and read back into a query. |
| `bifrost.security.secret-like-code` | A literal that looks like a credential, matched by pattern. | An API key or private key pasted into source. |
| `bifrost.correctness.*` | Code that is wrong on at least one path. | Dynamic evaluation of a string; unsafe deserialization; a resource acquired and still open at an exit of the procedure (`resource-lifecycle`); a Go data race; an overwritten value that is never read; a discarded pure result; a contradictory condition; an unreachable statement; a member access on a Python receiver whose class provably lacks it. |
| `bifrost.maintainability.*` | Code that will cost more than it should. | Dead code; oversized code. |
| Code smells | Patterns worth a second look. | Loop-invariant sorting; regular-expression compilation, file reads, parsing, database or network calls, subprocesses, or sleeps beneath nested loops. |

Rules are added and tightened across Bifrost releases. The
[Bifrost policy documentation](https://bifrost.brokk.ai/static-analysis-policies/)
describes each built-in pack and the rule language they are written in.

## Severity and certainty are different

**Severity** (`error`, `warning`, `note`) is the rule's judgment of how bad the
pattern is. `--fail-on` picks which severities fail a check; the default,
`finding`, fails on any new finding at all.

**Certainty** is Bifrost's judgment of whether this location is really an
instance of the pattern:

| Certainty | Meaning | What to do |
| --- | --- | --- |
| `definite` | Bifrost proved the pattern or relation holds at this location. | Fix it. |
| `possible` | The match depends on something Bifrost could not settle: an ambiguous receiver or dispatch, name-based resolution, several candidate declarations, or a "may" relation. | Read the code before changing it. It can be a false positive. |
| `heuristic` | A text-pattern match, such as a secret-like string. | Confirm it the same way. |

A `possible` finding is still reported and still fails the check, because an
agent that reads the code can usually settle it in seconds. When it is wrong,
[ignore it](/checks/false-positives/) and later checks leave it out.

## What a finding looks like

```text
src/main/java/orders/ExportServlet.java:48  request parameter reaches SQL text  definite · error
  ID: 0f8fad5b-d9cb-469f-a165-70867728950e
  bifrost.security.java.servlet-parameter-to-jdbc: req.getParameter("q") reaches
  Statement.executeQuery(String) through OrderQuery.build (OrderQuery.java:21).
```

A linter sees two files that each look fine. The data-flow rule sees one path,
and reports it once, on the line the change wrote, with the step that proves it.
A path through an open dispatch that Bifrost could not resolve is reported as
`possible`; a binding it could not resolve at all makes the rule inconclusive
rather than clean (see [Read a report](/checks/read-a-report/)).

## Languages

C, C++, C#, Go, Java, JavaScript, Kotlin, PHP, Python, Ruby, Rust, Scala, and
TypeScript. Coverage differs by language and by rule: each rule claims the
languages it has been validated on, and runs only there. The
[Bifrost documentation](https://bifrost.brokk.ai) records what each front end
resolves.

## What a check does not report

- Findings that were already in the code. They are counted in the summary
  (`72 already in the code`), never listed, and never fail the check.
- Findings in files the change does not write. The check analyzes the files
  the patch touches, in the context of the whole repository.
- Anything a model might say. There is no AI review in a check, and no code is
  sent to one.

The web app's own scans of a repository are a different surface: broader,
triggered by pushes and pull requests or on a schedule, and paid in credits.
See [The web app](/account/web-app/).
