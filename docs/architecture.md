# Architecture

This describes Kyro as it is in the alpha. [ROADMAP.md](ROADMAP.md) says how
well each part is tested; [security-model.md](security-model.md) says what
it does not protect against.

## One boot, step by step

```
client firmware                 Kyro                          your DHCP server
      |  DHCPDISCOVER (broadcast) |                                 |
      |-------------------------->|-------------------------------->|
      |  address                  |                                 |
      |<------------------------------------------------------------|
      |  boot server + boot file  |  proxyDHCP, port 67 and 4011
      |<--------------------------|
      |  TFTP: ipxe.efi           |
      |<--------------------------|
iPXE  |  DHCP again, as "iPXE"    |
      |  -> http://kyro/boot.ipxe |
      |  GET /boot?mac=..&uuid=..&serial=..
      |-------------------------->|  looks the client up
      |                           |
      |  not armed: "exit 1"      |  -> firmware boots the next entry: the disk
      |  armed:     install script|  -> record becomes "installing" first
      |  GET /wimboot, /i/<token>/boot.wim, start script, answer file
      |<--------------------------|
Windows PE
      |  net use \\kyro\install   |  SMB, read-only
      |  setup.exe /unattend:...  |
      |  ... Setup restarts the machine; each network boot gets "exit 1" ...
installed Windows
      |  POST /api/v1/callback/<token>
      |-------------------------->|  record becomes "provisioned"
```

1. **proxyDHCP.** The firmware broadcasts a DHCP request. Your DHCP server
   answers with an address. Kyro answers next to it with the boot
   information only, and never hands out addresses. Replies are broadcasts
   from an ordinary UDP socket, which is why no raw socket is needed.
2. **TFTP.** A UEFI client downloads a signed shim (`ipxe-shim.efi`), which
   then fetches the signed iPXE; both pass Secure Boot
   ([ADR 0005](adr/0005-secure-boot.md)). With `secure_boot: false` it
   downloads the unsigned `ipxe.efi` directly.
3. **iPXE.** iPXE asks again, is recognised by its user class and is given
   an HTTP address. From here on everything is HTTP.
4. **The decision.** iPXE sends the client's MAC address, SMBIOS UUID and
   serial number. Kyro looks for a record under each. Only an `armed` client
   gets the installer; the record is written as `installing` before the
   answer is sent. Everyone else gets `exit 1`, on which UEFI firmware goes
   on to its next boot entry.
5. **wimboot and Windows PE.** iPXE loads wimboot, `boot.wim` out of the
   ISO, a start script and the rendered answer file, all under a path that
   contains the token of this one installation.
6. **Setup.** The start script maps the install share and starts
   `setup.exe` from RAM with the answer file and `install.wim` from the
   share ([ADR 0001](adr/0001-windows-install-source.md)).
7. **Callback.** The answer file makes the installed system send one HTTP
   request to the callback address. Kyro marks the client `provisioned`.

## Components

| Package | Responsibility |
| ------- | -------------- |
| `cmd/kyro` | The CLI: `serve`, `doctor`, `image`, `profile`, `bootfiles`, `arm`, `disarm`, `clients`, `share` |
| `internal/config` | The YAML configuration and its validation |
| `internal/server` | Starts all services in one process |
| `internal/dhcp` | DHCP packet parsing and the proxyDHCP responder |
| `internal/tftp` | Read-only TFTP server |
| `internal/boot` | The HTTP side: iPXE scripts, the start script, per-installation files, the callback, the status page |
| `internal/state` | Client records and the state machine ([ADR 0004](adr/0004-client-state.md)) |
| `internal/images` | The image store; presents all ISOs as one file tree |
| `internal/udf` | Reads files out of an ISO without mounting it |
| `internal/wim` | Reads edition names out of a WIM file |
| `internal/profiles` | The profile store, validation, token substitution ([ADR 0002](adr/0002-profiles.md)) |
| `internal/smbshare` | Read-only SMB server ([ADR 0003](adr/0003-smb-server-library.md)) |
| `internal/bootfiles` | Downloads iPXE and wimboot and checks them against compiled-in checksums |
| `internal/doctor` | The checks of `kyro doctor` |
| `internal/fsutil` | Atomic file writes |

`web/` is a placeholder. There is no web interface beyond the read-only
status page.

## Data

Everything is under `data_dir` (`/var/lib/kyro`):

| Directory | Content | Written by |
| --------- | ------- | ---------- |
| `images/` | ISOs (or links to them) and one JSON file each | CLI |
| `profiles/` | One directory per profile: the answer file as imported, and its metadata | CLI |
| `boot/` | `ipxe.efi`, `undionly.kpxe`, `wimboot`, and for Secure Boot `ipxe-shim.efi` and `ipxe-signed.efi` | CLI (`kyro bootfiles fetch`) |
| `state/` | One JSON file per client | CLI and service, under a file lock |

The CLI works on these files directly and does not talk to the service, so
it works whether or not the service runs.

## Client states

`unknown`, `armed`, `installing`, `provisioned`, `failed`. ADR 0004 has the
diagram and the rules. The two that matter most:

- Only `armed` leads to an installation, and getting the installer ends
  being armed.
- Arming for a profile that wipes a disk needs `--confirm-wipe`.

## Third-party components

[iPXE](https://ipxe.org/) and [wimboot](https://github.com/ipxe/wimboot) are
separate projects under the GPLv2. Kyro uses their released binaries
unmodified. They are not in this repository and not in the release tarball;
`kyro bootfiles fetch` downloads fixed versions and refuses files whose
SHA-256 checksum differs from the one compiled into Kyro. See
[NOTICE](../NOTICE).

Kyro contains and downloads no Microsoft software.

## Listening addresses

| Port | Service | Where |
| ---- | ------- | ----- |
| UDP 67, 4011 | proxyDHCP | Boot interface |
| UDP 69 | TFTP | `address` |
| TCP 80 | Boot HTTP and callback | `address` |
| TCP 445 | Install share | `address` |
| TCP 8080 | Status page | `127.0.0.1` by default |

The boot HTTP server offers only what a booting client needs. The status
page is a separate listener so that it can stay off the boot network.
