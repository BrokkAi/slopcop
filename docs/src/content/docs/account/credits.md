---
title: Credits and what it costs
description: "A workspace gets 1,000 free checks each calendar month, then pays for each check with prepaid credits. Repositories are free. What happens when credits run out, and who pays nothing."
sidebar:
  order: 5
---

**A workspace gets 1,000 free checks each calendar month.** The month is the
calendar month in UTC. A check uses a free check while the month has one left,
whether an agent, CI, or a pull request ran it. There are no paid tiers and no
per-seat price.

**Each check after those costs 0.1 credits.** One credit is one US cent, so
that is $0.001 a check, charged when the check finishes. A check that fails is
not charged, and asking again for a check that already ran is not charged
again. Checks' fractions add up, and the balance goes down by a whole credit
each time they reach one.

**Repositories are free.** Adding or enabling a repository costs nothing.

Credits are prepaid. The workspace's owner buys them in packs on the
workspace's Credits page, and can have them bought automatically when the
balance falls below an amount the owner chooses. The current prices are listed
on the Credits page and on the [pricing page](https://slopcop.com/pricing).

## When credits run out

A workspace never goes into debt. Once the month's free checks are used and
the balance cannot pay for a check, the workspace is out of credits: every new
check of it is refused with `insufficient_credits` and a `topUpUrl` until
credits are added. The workspace's pull request checks pause, and the
workspace's owner is emailed. Checks resume once credits are added or the next
month's free checks arrive.

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
