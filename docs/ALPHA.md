# Plan for v0.1.0-alpha

The first version that does the whole job once: on a Linux server, add a
Windows 11 ISO and a profile, arm one client, and that client installs
Windows over the network without anyone touching it.

This file is the plan and the record of the small decisions made on the way.
What actually works is in [ROADMAP.md](ROADMAP.md), nowhere else.

## In scope

- Linux server, x86-64. UEFI clients with Secure Boot switched off.
  Secure Boot was added later as an experimental option
  ([ADR 0005](adr/0005-secure-boot.md)).
- The boot chain: proxyDHCP, TFTP, HTTP, iPXE, wimboot, Windows PE, and the
  SMB install share of [ADR 0001](adr/0001-windows-install-source.md).
- Images are ISOs, read in place. Profiles are answer files per
  [ADR 0002](adr/0002-profiles.md), with token substitution.
- One built-in example profile, marked as a test fixture.
- Arming a client for exactly one installation, a callback from the
  installed system, and the client states `unknown`, `armed`, `installing`,
  `provisioned`, `failed`.
- CLI: `kyro serve`, `image`, `profile`, `arm`, `disarm`, `clients`,
  `doctor`, `bootfiles`.
- A read-only status page and JSON endpoint.
- `install.sh`, a systemd unit, a Docker Compose example, a release build.
- An end-to-end test in QEMU.

## Out of scope

- Secure Boot was out of scope when this plan was written. See above.
- Windows and macOS as server platforms. External Samba (`smb.mode:
  external`).
- A web UI that changes anything. A generator for answer files.
- Driver injection, the hardware requirement bypass, ARM clients.
- Signed releases. The alpha has SHA-256 checksums only.
- BIOS clients and full DHCP are attempted last and only if cheap. Their
  status in the roadmap says whether they made it.

## Milestones

| No. | Milestone | Done when |
| --- | --------- | --------- |
| 1 | Foundations: configuration, reading ISOs in place, image store, profile store, client state | Unit and fuzz tests pass; `kyro image add` accepts the evaluation ISO |
| 2 | Boot services: proxyDHCP, TFTP, HTTP, iPXE scripts, start script, `kyro serve` | A virtual machine boots Windows PE from `kyro serve` alone |
| 3 | Arming, one-shot installs, callback, fall-through to disk | Acceptance tests A1 to A4 pass in QEMU |
| 4 | `kyro doctor`, installer, systemd unit, Compose example, release build | Acceptance tests A5 to A7 pass |
| 5 | Documentation, roadmap, release checklist, known limitations | A newcomer can follow the README from install to a provisioned client |

## Acceptance tests

| No. | Test | How |
| --- | ---- | --- |
| A1 | An armed client installs Windows 11 unattended and Kyro shows it as `provisioned` | `make e2e` |
| A2 | A client that is `installing` or `provisioned` and boots from the network again is sent to its local disk and is not reinstalled | `make e2e`, plus unit tests of the state machine |
| A3 | A client that was never armed gets no installer | `make e2e` (second virtual machine), plus unit tests |
| A4 | A profile that wipes a disk cannot be armed without `--confirm-wipe` | Unit tests |
| A5 | `install.sh` installs, is idempotent, uninstalls and purges, on Debian, Ubuntu and one RPM-based distribution | Containers |
| A6 | `install.sh` is clean under `shellcheck`; it installs only from a tarball whose checksum it verified | CI |
| A7 | The release build produces a tarball and a checksum file for linux/amd64 | `make release`, and the workflow on a tag |

`make e2e` needs KVM, about 25 GB of disk and a Windows 11 ISO that you
supply. It does not run in the default CI.

## Decisions

Larger decisions get an ADR. Small ones are listed here.

| Decision | Why |
| -------- | --- |
| ISOs are read in place with a UDF reader of our own (`internal/udf`). | No root rights for mounting, no second copy of 7 GB, no external tool. Windows ISOs are UDF 1.02 with one partition, which is a small subset to implement. |
| The CLI changes files directly and does not talk to the running service. | The CLI then works whether or not the service runs. See ADR 0004 for the storage. |
| The boot HTTP server and the status page listen on different addresses. The status page defaults to `127.0.0.1`. | The boot network is untrusted. It gets the boot endpoints and the callback, nothing else. |
| Files for an installation are served under a random token that exists only while the client is `installing`. | A host on the boot network can no longer fetch every profile by guessing names. It is still plain HTTP. |
| The DHCP replies are broadcast. | Then `CAP_NET_BIND_SERVICE` is the only capability needed. Unicast to a client without an address needs raw sockets. |
| iPXE is told to boot the disk with `exit 1`. | A UEFI boot manager goes on to the next entry only after a failure. Confirmed with OVMF in the end-to-end test. |
| iPXE and wimboot are downloaded by `kyro bootfiles fetch` with checksums that are compiled in. | One place for the pins, usable by the installer, the Compose example and by hand. |
| One install share (`install`) with a directory per image. | The share list then does not change when images are added, and the SMB server needs no restart. |
| The service reads images, profiles and boot files and writes only the client state. | The CLI, as root, changes the rest. A file the CLI writes gets the owner of its directory, so that the service can read it. |
| `install.sh` checks all input before it changes anything, and tries the new program where it will live. | A refused installation leaves nothing behind. `/tmp` may be mounted `noexec`. |
| `install.sh` starts the service only if `kyro doctor` found no problem. | A service that cannot bind its ports would restart in a loop. |
| The release tarball carries the license texts of everything compiled in. | MIT, BSD and Apache-2.0 ask for that when binaries are distributed. |
| In Docker, Kyro runs as root with all capabilities dropped but one. | Docker gives added capabilities to root only; file capabilities do not work with `no-new-privileges`. |
| The end-to-end test runs `kyro serve` as an unprivileged user with `CAP_NET_BIND_SERVICE` only. | So that the claim about capabilities is tested with a real installation. |
| YAML for the configuration, parsed with `go.yaml.in/yaml/v3` (MIT and Apache-2.0). | The format was fixed in the first brief. |

## Results

Filled in as the acceptance tests were run. Nothing here is estimated.

| No. | Result |
| --- | ------ |
| A1 | Passed three times. 2026-11-13: `provisioned` after 25 minutes, the whole test took 27. 2026-11-14, with Kyro unprivileged: after 36 minutes, 40 in all, while other work ran on the machine. 2026-11-14, with Secure Boot enforced: 32 minutes in all. |
| A2 | Passed in each of these runs: three network boots during the installation and one after a reset were sent to the disk; the installer was handed out once; Windows came up from the disk. |
| A3 | Passed in each of these runs: the second machine was sent to its disk, nothing was handed out, its disk stayed empty. |
| A4 | Passed: unit tests, and in the same run `kyro arm` without `--confirm-wipe` was refused and left no record. |
| A5 | Passed on 2026-11-14: `make test-installer` on Debian 13, Ubuntu 24.04 and Fedora 43 containers, and under systemd in a Debian 13 container. |
| A6 | `shellcheck` is clean locally and is a step of the CI workflow. The workflow itself has not run with these changes, because nothing was pushed. |
| A7 | `make release` passed locally and gives the same tarball twice. The release workflow has never run. |

Known oddity: iPXE waits about four seconds between its DHCP exchange and
its first HTTP request. A packet capture showed that it sends nothing to
port 4011 in that time. The cause was not found.

## Open questions for the maintainer

Each has a default that the work proceeded with.

1. **Bundle format.** The field list shared with the website was never added
   to ADR 0002. Default: a provisional format with `"bundle_version": 0`, so
   that it cannot be confused with the real one later.
2. **A fourth token.** The example profile has to know where to send the
   callback. Default: `{{callback_url}}`, filled in by Kyro, not by the
   client.
3. **Release signatures.** Default: none in the alpha, checksums only, listed
   as a known limitation.
4. **Defects in the SMB library.** ADR 0003 lists five. Whether and how to
   report them upstream is yours to decide.
