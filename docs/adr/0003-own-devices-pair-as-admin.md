# 0003: Your own devices pair as admins

**Status:** accepted, 2026-09-14. Amends r3 §8.2 and §8.7.

## Decision

Local pairing begins by asking whose machine is joining. A second question asks where it is,
which picks the mechanism rather than the role.

```text
Add a device
  whose?    ( ) My own device           → admin
            ( ) Someone else's device   → standard
  where?    ( ) Here with me            → code exchange (§8.2)
            ( ) Somewhere else          → invite by email (§8.3)
```

The two questions are independent. Someone else's laptop sitting on your table pairs with the
same code exchange your own laptop uses, and the only difference is the role it lands in. The
invite path exists because a person in another city has no screen you can read a code off, and
it runs through the cloud, so a local-only workspace cannot use it.

The role is fixed by the first answer. The approval screen still shows the key fingerprint before
anything is signed, and it now also states the role as a fact, so a misclick on the first
screen is caught on the last one.

An admin device joining this way co-signs its own acceptance during the pairing exchange,
which is what §8.7 already requires of a promotion. The channel is open and authenticated at
that moment, so it costs nothing extra; done later it is a separate round trip between two
machines that may no longer be next to each other.

## What went wrong with the old flow

§8.2 admitted every locally paired device as `standard`. §10.1 requires an admin device to
sign the request that turns remote access on. Put those together and the product had a
deadlock at the exact moment it wanted to sell something:

```text
laptop at a cafe   standard   notices it cannot reach home, cannot enable remote access
desktop at home    admin      could enable it, is the thing that is unreachable
```

The upgrade prompt appeared only where it could not be accepted. §8.7 half-saw this already,
with a UI that "recommends making it an admin" once a second desktop joins, but a
recommendation the user has to notice and act on before travelling is not a fix.

## Alternatives

| Alternative | Why not |
|---|---|
| Leave everything `standard`, promote later | Promotion needs an admin signature too, from the same machine sitting at home. Moves the deadlock, does not break it. |
| Ask the role on the approval screen instead of the first screen | Same roles, worse flow. Deciding after the code exists means the code cannot carry the role, and the approval screen ends up carrying two jobs: check a fingerprint, and grant authority. Those deserve separate moments. |
| Let the cloud enable remote access without an admin signature | Breaks principle 4 and D5. The cloud would be granting, which is the one thing the architecture exists to prevent. |
| Let a standard device start a trial, with an admin confirming afterwards | The confirmation would be pending on a machine the user cannot reach. That is the original problem with a delay in front of it. |

## Scope: local pairing only

Remote pairing (§8.3) still admits every device as `standard`, your own included. A workspace
that can pair remotely already has remote access on, which means an admin device signed the
request that turned it on. The deadlock described above cannot arise there, so nothing needs widening.

## Consequences

A typical workspace now holds several admin keys instead of one. That is a real widening of
who can rewrite the trust list, and it is worth being plain about what it does and does not
touch:

- It does not widen file access. §9 ends with "admin role grants no file access", and the
  access rule never reads the role. A stolen admin device reads exactly the files a stolen
  standard device reads.
- It does widen trust-list authority. A stolen own-device can add another device to the
  workspace until it is revoked. Revocation (§8.5) is unchanged and is enforced by peers as
  they learn the new version, so it still works against a device that never comes back.
- The last-admin rule in §8.7 is unchanged and now rarely bites, because there is normally
  more than one admin.
- Succession cover arrives by default, which was §8.7's goal. The recovery bundle (#11) stays
  opt-in and stays worth offering, since every admin being lost at once is still possible.

The case to watch in review is a user pairing a housemate's laptop and picking "my own
device" out of habit. The role on the approval screen is the guard, and #37 owns making it
legible rather than a detail row.
