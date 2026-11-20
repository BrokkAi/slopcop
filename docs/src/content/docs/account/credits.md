---
title: Credits and what it costs
description: "A workspace gets 1,000 free checks each calendar month, a team workspace once credits have been bought for it, then pays for each check with prepaid credits or has them included on the Team plan. Repositories are free. What happens when credits run out, and who pays nothing."
sidebar:
  order: 5
---

**A workspace gets 1,000 free checks each calendar month.** The month is the
calendar month in UTC. A check uses a free check while the month has one left,
whether an agent, CI, or a pull request ran it. Your personal workspace has them
from the start, unless your account's free checks are withheld: an address at a
throwaway mailbox provider, a second account with the same address, or one of
many sign-ups from one client get none, and the Credits page says why. A team
workspace gets them once credits have been bought for it; until then its checks
use credits, and the first team workspace you create starts with 100.

**Each check after those costs 1 credit.** One credit is one US cent, so
that is $0.01 a check, and $10 of credits buys 1,000 checks. A check is
charged when it finishes. A check that fails is not charged, and asking again
for a check that already ran is not charged again.

**Repositories are free.** Adding or enabling a repository costs nothing, and
there is no limit on how many a workspace has.

**Or the Team plan.** A team workspace can subscribe to the Team plan instead:
every check is included for everyone in it, with no free-check limit and no
credits used, for $24 a member a month billed annually or $30 billed monthly.
The owner subscribes on the workspace's Credits page; see the
[pricing page](https://slopcop.com/pricing).

Credits are prepaid. The workspace's owner buys them in packs on the
workspace's Credits page, and can have them bought automatically when the
balance falls below an amount the owner chooses. The current prices are listed
on the Credits page and on the [pricing page](https://slopcop.com/pricing).

## When credits run low

Every check's answer says what funds the workspace's next checks: the free
checks left this month, the credits available, and the price of a check (the
`credits` block in `--format json`). When fewer than 100 checks are left
across both, `slopcop check` prints one line on stderr:

```text
slopcop: 42 checks left on this workspace's free checks and credits; add credits at https://slopcop.com/credits
```

An agent's Stop hook says it once a session, and an agent that sees it tells
you once rather than after every check. A workspace that is never charged gets
no such line.

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
SlopCop system administrator, are never charged. A team workspace on the Team
plan pays for its members, not its checks.

## Scans in the web app

The web app's own scans of a whole repository, whether started by hand,
triggered by a push or pull request, or run over history, are priced in credits
and listed on the Credits page. They use the same monthly free checks and
credits as checks do. Scans are separate from checks: a check never starts a scan, and
never needs one to have run. See [The web app](/account/web-app/).

## Credits are the workspace's

Members share a workspace's credits and free checks. A personal workspace's
credits can be moved to a team the account belongs to. The first team
workspace an account creates gets 100 starter credits; a second one gets none.
