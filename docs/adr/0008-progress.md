# ADR 0008: Progress and failure reasons of an installation

- **Status:** Accepted on 2026-11-16.
- **Date:** 2026-11-16
- **Deciders:** Kyro maintainers

## Context

Between "the client was given the installer" and "the installed system
called back", Kyro knew nothing. A client was `installing` for up to
`install_timeout` (three hours) and then `failed`, with the reason "did not
report back". Whether Windows PE never got a network, Setup found no disk,
or the installed Windows had no network to call back with, all looked the
same, and the administrator had to walk to the machine to find out.

Kyro's part of an installation runs in two places. One is the start script
in Windows PE, where Kyro decides what happens. The other is everything
after Setup's first restart, where nothing of Kyro runs except what the
answer file does.

Windows PE has no program that sends an HTTP request (no `curl`, no
PowerShell; checked in the `boot.wim` of the Windows 11 ISO). What it has,
by the time anything can be reported, is a connection to the install
share.

## Decision

### Stages

The record of a client gets a **stage**, a time since when, and a count of
restarts. They come from two sources.

| Stage | How Kyro knows |
| ----- | -------------- |
| `installer-sent` | Kyro handed out the boot script. |
| `share-connected` | The start script reported that it connected the install share. So Windows PE runs and has a network. |
| `setup-started` | The start script reported that it is about to start Windows Setup. |
| `setup-finished` | The start script reported that Setup ended its part in Windows PE without an error. Setup usually restarts the machine itself, and this report does not come. |
| `restarted` | The machine asked Kyro what to boot again while it was `installing`. Setup restarts it several times; each time is counted. |

A stage is only replaced by a later one. `kyro clients`, the status page
and the JSON show the stage of a running installation in words.

### Reports from Windows PE

The start script reports by asking the install share for a file that does
not exist:

```bat
set /p KYRO_NONE=<\\kyro\install\kyro-report.<token>.share-connected 2>nul
```

Kyro's SMB server asks its file tree for that name; the tree notes the
report and answers that there is no such file. No write access to the
share is needed, and nothing is added to Windows PE.

The token of the installation is in the name, as it is in the address of
the callback. A report is accepted only while that installation runs.
Names of images have no dots, so no image can be called like a report.

### Failures

- **Setup ends with an error in Windows PE.** The start script reports
  `setup-failed` with Setup's exit code. The client becomes `failed` at
  once, not after three hours, with that code and the place of Setup's logs
  on the client.
- **The time runs out.** The reason now says where the installation
  stopped and what usually causes that:

  | Last stage | The reason names |
  | ---------- | ---------------- |
  | `installer-sent` | Windows PE never connected to the share: usually no driver for the network card, or no address. |
  | `share-connected`, `setup-started` | Setup started and the machine never restarted: usually no disk found (storage driver), or the answer file was rejected. |
  | `setup-finished`, `restarted` | Setup finished and restarted the machine N times, but nothing called back: usually no callback command in the answer file, no network driver in the installed Windows, or Setup waiting for input. |

  These are the usual causes, written as such. Kyro does not know which one
  applies.

## What this does not do

- **It is not authentication**, like the callback. Whoever knows the token
  can report a stage, or report that Setup failed and so end an
  installation's record as `failed`. The token travels in clear text on the
  boot network. Reports are limited in number, because each costs a look at
  every client record and anyone on the boot network can log in to the
  share.
- **No report can say that the share was not reached**: the share is how
  reports travel. That case is the absence of any report.
- **Setup's own reason is not transported.** The share is read-only, so
  Setup's logs stay on the client. Kyro passes on the exit code and says
  where the logs are.
- **Nothing is known about the time after the first restart** beyond the
  restarts themselves. A percentage of progress would be a guess; Kyro
  shows what it saw.

## Consequences

- Records, `kyro clients --json` and `/api/v1/clients` have new fields:
  `stage`, `stage_at`, `restarts`, and, while an installation runs,
  `progress`.
- The start script has four more lines. Each is a failed attempt to read a
  file and prints nothing.
- The log of the service gets one line per stage of each installation.
