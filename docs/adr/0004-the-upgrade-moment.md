# 0004: The upgrade moment

**Status:** accepted, 2026-09-14. Amends r3 §10.5 and §14. Depends on
[0003](0003-own-devices-pair-as-admin.md).

## Decision

Remote access gets a 14-day trial with no card, and it is offered on the device list of an
agent that has noticed it cannot reach the rest of its workspace.

```text
My Home
Local access     Not on this network
Remote access    Off

  Desktop     Unreachable
  Work PC     Unreachable

"These machines are somewhere else. Remote Access reaches them from anywhere."
                                              [ Try free for 14 days ]
```

The trial needs an account, so account creation happens here, at the first moment an account
buys the user anything. Install asks for nothing.

## Why this screen and not the web dashboard

The dashboard cannot do this job, and not merely because D2 says it shows no files. Before a
workspace is associated, the cloud does not know the workspace exists. There is no row to
render and no device to name.

The agent can, with no network at all. Every paired machine holds the full trust list (§8.1),
so the laptop in a cafe already knows Desktop exists, what it is called, and which folders it
shares. What it cannot know, until remote access is on, is whether a silent machine is switched off,
on another network, or behind a firewall. §16 therefore gives all three one word, `Unreachable`,
and the subscription is what earns the right to split them.

## Alternatives

| Alternative | Why not |
|---|---|
| Offer the trial during first run | Most trials would burn down while the user is at home on the LAN, where remote access changes nothing. The clock runs through the period where the feature is invisible, and expires around when it would first have mattered. |
| No trial, paywall on the Remote access row | Cheapest to build and no relay bandwidth spent on people who never convert. Rejected because it asks the user to pay to find out whether hole punching works on their connection, which is the one thing they cannot check first and the one thing most likely to disappoint. |
| Metered free tier, some GB per month | Strongest hook, but relay bandwidth for every free user forever, plus metering, quota enforcement and the support load of explaining a counter. Revisit if conversion from the trial is poor. |

## Abuse

One trial per account and one per workspace. Workspace IDs are minted locally by the creating
agent and cost nothing, so a per-workspace limit alone is worthless. Both limits together mean
a second trial needs a second email address, which is the usual floor and is where this stops.

## The flow end to end

```text
1  install, name the machine, pick folders            no account, LAN works
2  pair a second machine by code                      no account, LAN works
3  leave the house, open the app, see the offer       §14
4  sign up, trial starts, machines appear             relays authorized
5  convert, or expire into suspended                  §10.5
```

Membership is the separate path and it stays free. A person invited into someone else's
workspace makes an account, joins, and never pays, because §10.5 puts the whole workspace on
the owner's subscription.

## Consequences

- §22 has to accept an enable request backed by a trial rather than a paid subscription, and
  §21 has to carry `trial` through the same state machine.
- Trial expiry lands in `suspended` with no grace period. Grace exists to retry a failed
  charge, and there is no charge to retry.
- Agents cache subscription state for 24 hours (§10.5). A trial's final day therefore needs
  the cache treated as a floor and not a source of truth, or the last day stretches to two.
- Relay bandwidth is spent on trials that never convert. That is the cost being accepted here,
  and §18's per-workspace relay bytes is what will say whether it was worth it.
- This decision leans on 0003. Without admin devices in the user's pocket, the screen above
  renders on a machine that cannot act on it.
