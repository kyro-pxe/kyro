# Architecture (design plan)

> **This document is a design plan.** Nothing described here is implemented
> yet. Details will change as the implementation progresses.

## Goal

Boot Windows 11 setup on a PXE client directly from an ISO stored on the Kyro
server, with as little manual preparation as possible.

## Planned boot chain

```
DHCP / proxyDHCP  ->  TFTP / HTTP  ->  iPXE  ->  wimboot  ->  Windows PE / Setup
```

1. **DHCP / proxyDHCP.** The client's PXE firmware sends a DHCP request. Kyro
   answers with the boot server address and the boot file name. Two modes are
   planned:
   - *proxyDHCP*: an existing DHCP server keeps assigning addresses and Kyro
     only adds the boot information. This is the intended default, because it
     needs no changes to the existing network.
   - *full DHCP*: Kyro also assigns addresses, for isolated networks.
2. **TFTP / HTTP.** The firmware downloads the iPXE bootloader that matches its
   architecture and firmware type. TFTP is planned for firmware that supports
   nothing else, HTTP for UEFI HTTP boot and for everything after iPXE has
   started.
3. **iPXE.** iPXE requests a boot script from Kyro over HTTP. Kyro generates
   the script, including a menu of the available images.
4. **wimboot.** For the selected image, iPXE loads wimboot together with the
   boot files from the ISO (boot manager, BCD, `boot.sdi`, `boot.wim`) over
   HTTP.
5. **Windows PE / Setup.** wimboot starts Windows PE from `boot.wim`. Windows
   PE then needs access to the remaining installation files from the ISO to run
   Windows Setup. [ADR 0001](adr/0001-windows-install-source.md) proposes a
   read-only SMB share served by Kyro; the proposal is not yet accepted.

## Planned components

| Component        | Location          | Planned responsibility                                |
| ---------------- | ----------------- | ----------------------------------------------------- |
| CLI              | `cmd/kyro`        | Entry point, configuration, starting the services     |
| DHCP             | `internal/dhcp`   | DHCP and proxyDHCP responder                          |
| TFTP             | `internal/tftp`   | Read-only TFTP server for the iPXE bootloader         |
| iPXE             | `internal/ipxe`   | Generating iPXE boot scripts and menus                |
| Images           | `internal/images` | Discovering ISOs, reading their contents, per-image settings |
| Web UI           | `web`             | Managing images in the browser                        |
| Deployment files | `deploy`          | Docker image and systemd unit                         |

Today only `cmd/kyro` contains code, and it is a CLI skeleton. The `internal`
packages are empty placeholders.

## Images and unattended installation

- An image is a Windows 11 ISO provided by the user. Kyro does not ship or
  download Windows.
- Each image can optionally have its own `autounattend.xml`, which Kyro is
  intended to pass to Windows Setup so that an installation can run unattended.
- The web UI is intended for adding and removing images and for editing their
  settings, including the `autounattend.xml`.

## Third-party components

[iPXE](https://ipxe.org/) and [wimboot](https://github.com/ipxe/wimboot) are
separate projects licensed under the GPLv2. Kyro is intended to use them as
separate, unmodified binaries. They are not part of this repository; see
[NOTICE](../NOTICE). How Kyro obtains these binaries (download, packaging,
user-supplied) is not decided yet.

Design decisions are recorded in [docs/adr](adr/README.md). The status of
each planned item is tracked in [ROADMAP.md](ROADMAP.md).

## Open questions

- How Windows PE gets the installation files (`install.wim` or `install.esd`)
  from the ISO. [ADR 0001](adr/0001-windows-install-source.md) compares the
  options and proposes SMB; it awaits approval and a validation spike.
- UEFI Secure Boot support. ADR 0001 describes the constraints.
- Which client platforms to support first (UEFI x86-64 is the obvious starting
  point; legacy BIOS and ARM64 are undecided).
- Configuration format and where state is stored.
- Authentication for the web UI.
- How iPXE and wimboot binaries are obtained and updated.
