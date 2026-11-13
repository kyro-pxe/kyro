# ADR 0004: Client state and where it is stored

- **Status:** Accepted on 2026-11-14 for the alpha.
- **Date:** 2026-11-14
- **Deciders:** Kyro maintainers

## Context

Kyro installs Windows on machines that boot from the network. An answer file
that partitions a disk destroys whatever was on it. The one mistake Kyro
must not make is to hand such an installer to a machine that nobody meant to
install, or to hand it out again after the installation has finished.

Two programs need the state: the running service, which answers booting
clients, and the CLI, with which an administrator arms a client or looks at
the list. They usually run as different users.

## Decision

### States

| State | Meaning | A network boot gets |
| ----- | ------- | ------------------- |
| `unknown` | Seen on the network, never armed, or disarmed again | The local disk |
| `armed` | Will be installed on its next network boot | The installer, once |
| `installing` | Was given the installer, has not reported back | The local disk |
| `provisioned` | The installed system reported back | The local disk |
| `failed` | Did not report back within `install_timeout` | The local disk |

```
            kyro arm                 network boot              callback
 unknown ───────────────▶ armed ───────────────▶ installing ───────────▶ provisioned
    ▲                       │                        │
    └────── kyro disarm ────┘                        └── install_timeout ──▶ failed

 kyro arm leads back to armed from every state. From installing it needs --force.
```

**A client gets the installer only when it is armed, and getting it ends
being armed.** The record is changed to `installing` and written to disk
before the answer goes out. If writing fails, the client gets nothing.
Windows Setup restarts the machine several times; each of those boots, and
every boot after that, is answered with "boot your local disk". Only an
administrator arming the client again leads to another installation.

A client without a record is treated like `unknown`. Whether such clients
see a menu first is a setting (`unknown_clients`, default `local`); the menu
offers nothing that changes the machine.

### Selecting a client

A client is armed by its MAC address, its SMBIOS UUID or its SMBIOS serial
number. The booting machine sends all three, and Kyro looks for a record
under each.

**This is selection, not authentication.** A machine can claim any of the
three. Arming says "the next machine that shows this identifier is
installed". On a network where someone might answer in its place, that
someone gets the installer and whatever the answer file contains.

Serial numbers that firmware reports when none was set ("To be filled by
O.E.M.", "Default string", all zeros and the like) are refused as keys, and
so are the all-zero and all-one UUID and MAC address. Many machines share
them.

### The token

Arming creates a random 128-bit token. The files of that one installation
(the start script, the rendered answer file, `boot.wim`) are served under a
path that contains it, and the callback is addressed with it. The token
opens anything only while the client is `installing`.

It keeps a host on the boot network from fetching answer files by guessing
names. It is sent over plain HTTP, so it does not protect against someone
who can read the traffic.

### Storage: one JSON file per client

`<data_dir>/state/<kind>-<value>.json`, written by creating a temporary
file, syncing it and renaming it over the old one. Every change happens
under an exclusive lock on `<data_dir>/state/.lock` (`flock`).

Considered and rejected for the alpha:

- **SQLite.** The natural choice for state shared between processes. The
  pure Go implementation (`modernc.org/sqlite`, BSD-3-Clause) is a large
  dependency and would roughly double the binary; the usual one needs cgo,
  which gives up the static binary. For a table with one row per client and
  a handful of changes per installation that is a poor trade.
- **The CLI talks to the service over a socket.** One writer and no file
  locking, but then nothing can be armed, listed or repaired while the
  service is down, and the service needs a second API to secure.

What flat files cost:

- Looking up a token reads every record. With a few thousand clients that
  is still milliseconds; it happens once per file an installing client
  fetches.
- No history. A record holds the current state and a few timestamps.
- Both programs must be able to write the directory. The CLI, when run as
  root, gives new files the owner of the directory so that the service can
  still change them.

Records of clients that were never armed are written because of a request
from the network. Their number is capped at 1,024; beyond that, unknown
clients are still answered but not remembered.

## Consequences

- The service needs write access to `<data_dir>/state` and to nothing else.
- A damaged record is an error, not an empty list: Kyro stops answering
  boots rather than guess.
- If the number of clients or the need for history outgrows this, the
  package `internal/state` is the only place that has to change.
