---
title: Credits and what it costs
description: "Checks are free, and so is a workspace's first repository. Each active repository after that costs credits every 30 days. What happens when credits run out, and who pays nothing."
sidebar:
  order: 5
---

**Checks are free.** A check of a change from the CLI, whether an agent or CI
ran it, never uses credits, in every workspace. There are no paid tiers and no
per-seat price.

**Your first repository is free.** A workspace's oldest active repository costs
nothing.

**Each active repository after that costs 100 credits per 30 days.** One credit
is one US cent, so that is one dollar a month per repository, charged one UTC
day at a time (three or four credits a day, adding up to exactly 100 across 30
days). A disabled repository, or one being deleted, costs nothing. The current
price is listed on the workspace's Credits page and on
[slopcop.com](https://slopcop.com).

## When credits run out

A day is paid in full or not at all, and a workspace never goes into debt.
When the available balance cannot pay the day for its repositories, nothing is
charged and the workspace is out of credits: every new check of it is refused
with `insufficient_credits` and a `topUpUrl`, the free repository's checks too,
until credits are added. The first check after a top-up pays the day and runs.
A day that went unpaid is never charged later.

An agent that hits `insufficient_credits` stops and tells you; the printed link
opens the Credits page. Only the workspace's owner buys credits.

## Who pays nothing

A workspace an operator has marked Enterprise, and a workspace owned by a
SlopCop system administrator, are never charged.

## The active-repository limit

A workspace that has never paid (never topped up, had a coupon applied, or
been unlocked by an operator) keeps every repository active for its first 45
days, then one, until it unlocks. Any purchase or applied coupon unlocks it for
good: a workspace that has paid once stays unlocked at a zero balance, and
nothing it has is taken back. See [Repositories](/account/repositories/).

## Scans in the web app

The web app's own scans of a whole repository, whether started by hand,
triggered by a push or pull request, or run over history, are priced in credits
and listed on the Credits page. Every personal workspace gets a daily allowance
of free scans. Scans are separate from checks: a check never starts a scan, and
never needs one to have run. See [The web app](/account/web-app/).

## Credits are the workspace's

Members share a workspace's credits and free scans. A personal workspace's
credits can be moved to a team the account belongs to. Creating a team
workspace grants it starter credits once per account.
