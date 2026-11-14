# Roadmap

This file lists what Kyro is meant to do and how far each item actually is.
It is updated in the same commit that changes an item's state.

Status values:

- **planned**: not started.
- **in progress**: being worked on, not usable.
- **working**: implemented and works when tried by hand.
- **tested**: working, and covered by automated tests or a documented,
  reproducible test run.

"Tested" never means more than the notes say. In particular, every
installation of Windows so far happened in a virtual machine (QEMU with
OVMF) with one ISO, the Windows 11 Enterprise evaluation. Nothing was tried
on physical hardware.

The end-to-end test (`make e2e`, [scripts/e2e](../scripts/e2e/README.md)) is
referred to as "e2e" below. It passed six times:

| Date | Variant | Time |
| ---- | ------- | ---- |
| 2026-11-13 | Secure Boot off, Kyro as root | 27 minutes |
| 2026-11-14 | Secure Boot off, Kyro as an unprivileged user with `CAP_NET_BIND_SERVICE` only | 40 minutes, with other work on the machine |
| 2026-11-14 | Secure Boot enforced, Kyro unprivileged, `secure_boot: true` | 32 minutes |
| 2026-11-14 | Secure Boot off, Kyro unprivileged, with screen recording | Not comparable: the script waited at its end because of a defect in the recorder, fixed since |
| 2026-11-14 | Secure Boot enforced, Kyro's defaults (signed shim and iPXE), recorded in real time | 31 minutes |
| 2026-11-14 | Secure Boot off on the machines, Kyro's defaults (signed shim and iPXE) | 27 minutes |

The first, second and fourth run used the unsigned iPXE, which is now what
`secure_boot: false` gives. The last two are what Kyro does by default.

## Design

| Item | Status | Notes |
| ---- | ------ | ----- |
| How Windows Setup gets its install source | tested | [ADR 0001](adr/0001-windows-install-source.md): SMB, embedded by default. |
| Which SMB server to embed | tested | [ADR 0003](adr/0003-smb-server-library.md): `jfjallid/go-smb` with Kyro's own read-only file system. |
| Profiles | working | [ADR 0002](adr/0002-profiles.md), accepted for the alpha. The bundle format is provisional (`bundle_version` 0); the specification shared with the website is still missing. |
| Client state | tested | [ADR 0004](adr/0004-client-state.md): one JSON file per client. |

## Network boot

| Item | Status | Notes |
| ---- | ------ | ----- |
| proxyDHCP responder | tested | Unit and fuzz tests. In e2e next to a separate DHCP server (dnsmasq). Not tried next to other DHCP servers or through DHCP relays. |
| Full DHCP mode | planned | `dhcp.mode: full` is refused with a message. |
| DHCP switched off (`dhcp.mode: off`) | working | Kyro then expects your DHCP server to point at it. Unit tests only; no boot was tried this way. |
| TFTP server | tested | Read-only, block size and transfer size options. Unit tests; e2e; in the installer test under systemd. |
| HTTP boot server | tested | iPXE scripts, wimboot, the files of an installation, the callback. Unit and fuzz tests; e2e. |
| UEFI x86-64 clients, Secure Boot on | tested | The default: signed shim, signed iPXE, signed wimboot, all unmodified from upstream ([ADR 0005](adr/0005-secure-boot.md)). Two e2e runs with OVMF and Microsoft's keys enrolled; each first showed that the firmware refuses the unsigned iPXE. No physical machine. Machines that do not trust Microsoft's key for other vendors' boot loaders will refuse the shim. |
| UEFI x86-64 clients, Secure Boot off | tested | With the default signed files: one e2e run. With the unsigned iPXE (`secure_boot: false`): three e2e runs. OVMF in QEMU only. |
| BIOS x86-64 clients | in progress | A BIOS client is offered `undionly.kpxe`. No BIOS client was ever booted. |
| Obtaining iPXE and wimboot | tested | `kyro bootfiles fetch` downloads fixed versions from the projects' release pages and checks them against compiled-in checksums. Unit tests; one real download on 2026-11-14. Never committed, never rebuilt. |
| YAML configuration | tested | Unit tests. Unknown keys are errors. |
| `kyro serve`: all services in one process | tested | Integration test on loopback; e2e. |
| Fall-through to the local disk (`exit 1` in iPXE) | tested | e2e: OVMF goes on to the disk. Other firmware may behave differently. |

## Images and profiles

| Item | Status | Notes |
| ---- | ------ | ----- |
| Reading files out of an ISO in place | tested | Own UDF reader (`internal/udf`). Unit and fuzz tests; all 978 files of the evaluation ISO compared with a kernel mount. Only what Windows ISOs use: UDF 1.02, one partition. |
| `kyro image add`, `list`, `remove` | tested | Copies or links (`--link`) an ISO, checks that it is a Windows installation medium, lists its editions. Unit tests; the evaluation ISO. |
| Read-only SMB server (`internal/smbshare`) | tested | SMB 3.1.1, signing required, one account. Unit tests, tests with a real client, fuzz tests. Five defects found in the library are listed in ADR 0003. |
| Install share: one share, one directory per image | tested | e2e. |
| Start script for Windows PE | tested | Maps the share with retries, copies `ei.cfg` and `pid.txt`, starts Setup from RAM with the answer file. Unit tests; e2e. |
| Closing a client's old SMB connections when it is installed again | working | Called when an installation starts. Not covered by a test of its own. |
| `kyro share`: the SMB server on its own | tested | For testing. |
| `kyro profile import`, `list`, `show`, `remove` | tested | Bare `autounattend.xml` or a bundle. Unit and fuzz tests. |
| Built-in example profile | tested | A test fixture: wipes disk 0, public password. e2e installs with it. Made for the evaluation ISO. |
| Token substitution (`{{mac}}`, `{{serial}}`, `{{uuid}}`, `{{callback_url}}`) | tested | Unit and fuzz tests. `{{mac}}` and `{{callback_url}}` in e2e; `{{serial}}` and `{{uuid}}` in unit tests only. |
| Warnings of a profile shown at import and when arming | tested | Unit tests. |
| Detecting that a profile wipes a disk | tested | Declared in the bundle, or detected in the XML; a bare answer file counts as wiping. Unit tests. |
| Menu of images and profiles for a person at the client | planned | By design no menu can start an installation in this version. |
| Hardware requirement bypass (LabConfig) per image | planned | Opt-in, see ADR 0001. Until then your answer file has to deal with it. |
| NIC driver injection | planned | |
| External SMB server (`smb.mode: external`) | planned | |
| SMB port other than 445 | working | `smb.port`. Works with Windows PE of 24H2 and later only; verified by hand in the spike for one build, not in e2e. |

## Arming and client state

| Item | Status | Notes |
| ---- | ------ | ----- |
| `kyro arm`, `disarm`, `clients` | tested | By MAC, UUID or serial. Unit tests; e2e by MAC. Arming by UUID or serial was not tried with a booting machine. |
| One installation per arming, no reinstall loop | tested | Unit and fuzz tests of the state machine; e2e: Setup's restarts and a reset after the installation all went through the network and were sent to the disk. |
| An unarmed client gets no installer | tested | Unit tests; e2e with a second machine. |
| `--confirm-wipe` for profiles that wipe a disk | tested | Unit tests; e2e. |
| Callback from the installed system | tested | Unit and fuzz tests; e2e. Rate and size limited. Not authentication. |
| `failed` after `install_timeout` | tested | Unit tests only. |
| Menu for unknown clients (`unknown_clients: menu`) | tested | Off by default. Unit tests only; shows identifiers and boots the disk. |
| Read-only status page and JSON (`/api/v1/clients`) | tested | Unit tests. Listens on `127.0.0.1:8080` by default. |

## Running Kyro

| Item | Status | Notes |
| ---- | ------ | ----- |
| `kyro doctor` | tested | Configuration, interface, ports, conflicting services, firewalls (prints commands, changes nothing), boot files, disk space. Unit tests with described machines; run by hand in the e2e network and in containers. The firewall and service checks were not run against real ufw, firewalld or Samba installations. |
| `install.sh` | tested | `make test-installer`: Debian 13, Ubuntu 24.04 and Fedora 43 containers; under systemd in a Debian 13 container. Never run on a real server, and never against a real release page, because there is no release yet. |
| systemd unit with `CAP_NET_BIND_SERVICE` only | tested | Capabilities and listening ports checked on the running service in the installer test. e2e installs Windows from a Kyro that runs as an unprivileged user with that one capability and the installer's file permissions; it is started by `setpriv` there, not by systemd, so the unit's other sandbox settings were not part of an installation. |
| Release build | tested | `make release`: tarball, installer, `SHA256SUMS` for linux/amd64; the same commit gives the same tarball. The GitHub workflow that attaches them to a draft release has never run: it runs only on a pushed `v*` tag, and none exists yet. A manual dry run is possible ([releasing.md](releasing.md)). |
| Docker image and Compose example | working | Built and run on a private bridge network. Not tried with host networking or a booting client. |
| End-to-end test in QEMU | tested | `make e2e`. Six passing runs, see above. |
| Signed releases | planned | Checksums only. |
| linux/arm64 server | planned | |
| Windows and macOS as server | planned | |

## Management

| Item | Status | Notes |
| ---- | ------ | ----- |
| Web UI that changes anything | planned | |
| REST API that changes anything | planned | |
| History of installations | planned | A record holds the current state only. |
