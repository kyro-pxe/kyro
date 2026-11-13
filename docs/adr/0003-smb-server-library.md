# ADR 0003: SMB server library

- **Status:** Accepted on 2026-11-13. The maintainer chose the library that
  passed the validation spike. How it is used, and what was found when it was
  fuzzed, is recorded below.
- **Date:** 2026-11-13
- **Deciders:** Kyro maintainers

## Context

[ADR 0001](0001-windows-install-source.md) decided that Windows Setup reads
the install image from a read-only SMB share, and that on Linux Kyro serves
that share itself. It left the implementation open and set two conditions:
only MIT, BSD or Apache-2.0 licensed code, and fuzz tests for whatever is
chosen.

The [validation spike](../spikes/0001-install-source.md) tried four
MIT-licensed Go libraries against Windows PE. Two could not be reached by
Windows at all. Of the other two, only `github.com/jfjallid/go-smb` completed
an installation with both ways of starting Setup. Both failed a wildcard
listing (`dir install.*`) that Samba answers.

## Options

- **A. `jfjallid/go-smb` with the file system it ships.** What the spike
  tested. Its file system can also write, and it matches only `*` or an exact
  name in directory searches.
- **B. `jfjallid/go-smb` for the protocol, with Kyro's own file system and
  settings.** The library has an interface for the file system behind a
  share. Kyro implements it and decides what a client can reach.
- **C. A server of our own.** Setup started from RAM needs 5 files and 14
  open requests, so a minimal server is conceivable. It would still have to
  implement negotiation, NTLMv2, key derivation and signing correctly, and
  would be new, unreviewed code in exactly the part where mistakes matter
  most.
- **D. `sirrobot01/facetfs`.** Installs only with Setup started from RAM and
  matches names with regard to case.

## Decision

**B.** The protocol comes from `github.com/jfjallid/go-smb`, pinned to
v0.12.0. Everything that decides what a client can see or do is Kyro's code,
in `internal/smbshare`.

### What Kyro owns

- **The file system behind a share.** It serves an `fs.FS`. That interface
  has no operation that changes anything, so there is no code path that
  writes, and none that could be reached by mistake. A directory on disk is
  opened through `os.Root`, which refuses every path that leaves it.
- **Opening.** Only existing files and directories can be opened. A request
  that asks for any write, delete or ownership right is refused, so no
  handle with such a right exists. This is what Samba does for a read-only
  share.
- **Names.** Lookups ignore case and return the stored name. A path element
  is refused if Windows would refuse it as a file name: `..`, `/`, `:`,
  wildcards, control characters. Only directories and regular files are
  visible. Symbolic links are neither followed nor listed, even if they point
  inside the share. Devices, sockets and pipes are invisible.
- **Search patterns.** Kyro implements the wildcard rules of MS-FSA 2.1.4.4,
  including the three characters that Windows substitutes before it sends a
  pattern. `dir install.*` arrives as `install"*`. This was the cause of the
  failed listing in the spike.

### Settings

| Setting | Value | Why |
| ------- | ----- | --- |
| Dialect | SMB 3.1.1 only | Protects the negotiation itself. Every Windows version Kyro installs speaks it. This narrows the "SMB 2.1 to 3.1.1" of ADR 0001. |
| Signing | Required | |
| Accounts | One, NTLMv2 | Not a secret, see [the security model](../security-model.md). |
| Guest, anonymous | Off | |
| Encryption | Off | The content is public and so is the account. |
| Compression, durable handles, named pipes | Off | Not needed. Without named pipes a client cannot list shares or ask anything over RPC. |

The account name and password are limited to letters, digits, `.`, `_` and
`-`, at most 64 characters. They are written into a batch file on a `net use`
command line, and this leaves nothing to escape. Share names are limited in
the same way.

### Limits

| Limit | Default |
| ----- | ------- |
| Connections at the same time | 256 |
| Open files and directories, all clients together | 4,096 |
| Connection that sends nothing | closed after 30 minutes |
| Client that stops responding | connection closed after about 30 seconds (TCP keepalive, and `TCP_USER_TIMEOUT` on Linux) |

The last one addresses the stale sessions found in the spike. In addition
the server can close all connections of one address. Kyro will call that when
a client starts to boot.

### Dependencies

These modules are compiled into the Kyro binary. All are listed in
[NOTICE](../../NOTICE).

| Module | Licence |
| ------ | ------- |
| `github.com/jfjallid/go-smb` | MIT |
| `github.com/jfjallid/gofork` | BSD-3-Clause (fork of parts of the Go standard library) |
| `github.com/jfjallid/gokrb5/v9` | Apache-2.0 |
| `github.com/jfjallid/golog` | MIT |
| `github.com/jfjallid/mstypes` | Apache-2.0 |
| `github.com/jfjallid/ndr` | Apache-2.0 |
| `golang.org/x/crypto` | BSD-3-Clause |
| `golang.org/x/net` | BSD-3-Clause |
| `software.sslmate.com/src/go-pkcs12` | BSD-3-Clause |

The library is a client, a server and a set of security tools in one module.
Its SMB package imports the Kerberos code, so that is linked into Kyro
although the server only accepts NTLM.

## What fuzzing found

The fuzz tests in `internal/smbshare` send bytes to a running server: raw
bytes, messages before the login, and messages inside a logged-in, signed
session. The account is public, so the third group is as reachable for an
attacker as the first two.

They found five defects in v0.12.0 of the library, the first within seconds.

| # | Where | Reachable | Effect | In Kyro |
| - | ----- | --------- | ------ | ------- |
| 1 | `NegotiateReq.UnmarshalBinary` does not check the dialect count and the context offset against the message length | Before login | Panic | Contained by the library |
| 2 | `CreateReq.CreateReqName`: 32-bit offset arithmetic wraps around | After login | Panic | Contained by the library |
| 3 | `CreateReq.CreateReqContexts`: the same | After login | Panic | Contained by the library |
| 4 | A logoff forgets the session's open files without closing them | After login | Open files accumulate until the process runs out | Worked around |
| 5 | `Serve` leaves the listener open if the server was closed first | Not by a client | Port stays bound | Worked around |

**1 to 3** are out-of-range reads in a memory-safe language. They cannot
corrupt memory or disclose it. The library recovers from a panic in the code
that serves a connection and closes that connection. The sender loses its own
connection and nothing else happens. Kyro logs each one as an error with its
stack trace, at most five per minute. The fuzz tests know these three by
function name, tolerate them and fail on any other panic.

**4** is the serious one. Because anyone can log in, a client could log in,
open files and log off in a loop until the server had no handles left for
anyone. Kyro keeps its own table of open handles per session and closes them
when a session logs off and when a connection ends. A test fails without
that.

What this says about the library: its request parsers had evidently not been
fuzzed. More defects of the kind of 1 to 3 are likely. They are contained as
long as the recovery in the library stays in place. Fuzzing does not show
logic errors in authentication or signing.

How long the fuzz tests have run without a new finding is recorded in
[docs/ROADMAP.md](../ROADMAP.md).

## Verification

- Unit tests for the file system and the wildcard rules, the latter also by
  fuzzing against a second, independent implementation.
- Tests that run a real SMB client against the server: reading, listing,
  every kind of write, wrong and missing credentials, older dialects,
  unsigned sessions, unknown shares, the limits.
- `smbclient` from Samba, by hand: the same, with the same results.
- Windows PE, in the harness of the validation spike: two unattended
  installations of Windows 11 from `kyro share` completed, one with Setup
  started from RAM and one with Setup run from the share. Each ran once,
  with one ISO, in QEMU. See
  [the spike report](../spikes/0001-install-source.md#follow-up-kyros-own-server).

## Consequences

- The Kyro binary contains nine third-party modules, where it had none.
- The library is pinned. An update is a reviewed change: run the fuzz tests,
  check the list of known panics, and repeat the installation test of
  `scripts/spike-install-source` against `kyro share`.
- The embedded server stays behind the limits that the security model
  already sets: bound to the boot network, read-only, possible to switch off
  (`smb.mode: external` or `off`).
- Reasons to reopen this decision: a crash that the library does not
  recover from, a way around authentication or out of the share, or a
  library that is no longer maintained. The alternatives are then C for the
  RAM start only, or an external server by default.

## Open questions

1. **Reporting the defects upstream.** Not done. Defect 1 can be triggered
   without credentials against any server built on the library, so it should
   go to the maintainer privately first.
2. **Reading the ISO directly.** ADR 0001 plans to serve files from the ISO
   by offset. The file system here takes any `fs.FS` whose files can be read
   at an offset, so a UDF reader fits. It does not exist yet; for now the
   share serves a directory.
