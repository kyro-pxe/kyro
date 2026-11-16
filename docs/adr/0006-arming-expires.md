# ADR 0006: An arming runs out

- **Status:** Accepted on 2026-11-17.
- **Date:** 2026-11-17
- **Deciders:** Kyro maintainers

## Context

Arming says "install this machine on its next network boot"
([ADR 0004](0004-client-state.md)). Until now it said so for ever. A
machine that was armed in March and not switched on, or that booted from
its disk because the boot order was wrong, stayed armed. Whenever it did
boot from the network, weeks or months later, it was wiped. By then nobody
remembers the arming, and the machine may be in use.

The same holds for an arming by serial number or UUID of a machine that was
swapped, and for a typing mistake that armed an address which a different
machine gets later.

## Decision

**Every arming has an end.** The record of an armed client says until when
(`armed_until`). A client that asks what to boot at or after that moment is
not installed. Its record changes to a new state, `expired`, and it is sent
to its disk like every client that is not armed.

| Setting | Meaning | Default | Limits |
| ------- | ------- | ------- | ------ |
| `arm_timeout` in the configuration | How long an arming lasts | `24h` | `1m` to `30d` |
| `kyro arm --expires <time>` | The same, for this one arming | `arm_timeout` | `1m` to `30d` |

Times are written like `90m`, `2h` or `7d`.

- **Where it is decided.** At the moment the client asks what to boot,
  under the lock on the state, with the clock of that moment. The check
  that the service runs once a minute, and that `kyro clients` runs, only
  makes the state show up without a boot; the decision does not depend on
  it.
- **What `expired` keeps.** The image and the profile the client was armed
  for, and a note until when. The token of the arming is deleted; it opens
  nothing.
- **What leads out of `expired`.** `kyro arm`, as from every state. `kyro
  disarm` does not apply: there is nothing left to take back.
- **The end is fixed when arming.** Changing `arm_timeout` later does not
  prolong or shorten armings that exist. Arming again does.
- **Records without an end.** Version 0.1.0-alpha wrote armed records
  without `armed_until`. They are treated as armed for 24 hours from the
  time they were armed. A record that says neither is treated as run out.
  In doubt, no installation.

```
            kyro arm                 network boot              callback
 unknown ───────────────▶ armed ───────────────▶ installing ───────────▶ provisioned
    ▲                     │   │                      │
    └──── kyro disarm ────┘   └─ time is up ─▶ expired   └── install_timeout ──▶ failed
```

### Why there is no "never"

An arming without an end is exactly the accident this record is about, and
a setting that allows it would be switched on once and forgotten. Thirty
days cover a batch of machines that is prepared ahead of a rollout. Whoever
needs longer can arm again; that is one command, and it is the moment at
which someone confirms that the machine is still meant.

### Why 24 hours

Arming and switching the machine on usually happen in one sitting. A day
covers "arm today, the machines are switched on tomorrow morning" without
keeping anything armed over a weekend.

### Why a state of its own

`failed` means an installation was started and did not report back; the
disk of such a machine was probably changed. `unknown` means nobody ever
wanted this machine installed. An administrator who finds a machine not
installed needs to see the third case: it was meant to be installed, it did
not boot in time, and nothing was touched.

## What this does not do

- It is a protection against a forgotten arming, not against an attacker.
  Within the lifetime everything in [the security
  model](../security-model.md) applies unchanged: whoever claims the
  identifier gets the installation.
- It relies on the clock of the server. A clock that jumps forward ends
  armings early, which is the safe direction. A clock that is set back, or
  a server without a battery-backed clock that starts in the past, keeps
  them alive for longer. Kyro does not detect that.

## Consequences

- A client that was armed and is switched on a day later is not installed.
  `kyro clients`, the status page and the screen of the client (in the
  iPXE message) say why.
- `kyro clients --json` and `/api/v1/clients` have a new field,
  `armed_until`, and a new value of `state`, `expired`.
- `kyro arm` prints until when the arming lasts.
- The fuzz test of the state machine also checks that no installation
  starts after the end of its arming.
