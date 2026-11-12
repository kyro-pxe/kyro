# Spike 0001: Windows Setup over SMB

Validation spike for [ADR 0001](../adr/0001-windows-install-source.md),
run on 2026-11-12. The harness is in
[scripts/spike-install-source](../../scripts/spike-install-source/README.md).

This is a record of what was tried and what happened. It describes a test
setup built from dnsmasq, a Python HTTP server and Samba. It does not describe
anything Kyro can do yet.

## Result

The decision in ADR 0001 holds. A stock Windows 11 `boot.wim`, booted through
wimboot with injected files, installs Windows from an SMB share with no
change to any Microsoft file.

| No. | Criterion from ADR 0001 | Result |
| --- | ----------------------- | ------ |
| 1 | Stock `boot.wim` boots via wimboot with injected `winpeshl.ini` and script | Pass |
| 2 | `net use` against a signing-required Samba share with a named account | Pass |
| 3 | Both ways of starting Setup, both front ends, reach disk selection and install | Pass, with the limits listed under [Starting Setup](#starting-setup) |
| 4 | `/unattend:` with an injected answer file is honoured | Pass |
| 5 | `net start msiscsi` in stock Windows PE | Fail: the service does not exist |
| 6 | The same against embedded server candidates | Two of four candidates connect. Both install with Setup from RAM, one of them also with Setup from the share. See [Embedded server candidates](#embedded-server-candidates) |

## Test setup

- Host: Debian 13, kernel 6.12, QEMU 10.0.13 with KVM, OVMF 2025.02 (the
  Secure Boot capable build, with Secure Boot not enabled), swtpm 0.7.1.
- Virtual machine: q35, 4 cores, 4 to 8 GB RAM, Intel e1000e network card,
  SATA disk, TPM 2.0 unless stated.
- Boot chain: OVMF PXE, upstream iPXE 2.0.0 (`ipxe.efi`) over TFTP, wimboot
  2.9.0 and `boot.wim` over HTTP.
- SMB server: Samba 4.22.11, once with `server signing = mandatory` and a
  named account only, once with default signing and guest access.
- Windows: the Windows 11 Enterprise evaluation ISO from the Microsoft
  Evaluation Center,
  `26300.9457.260913-1737.26h2_ge_release_svc_refresh_CLIENTENTERPRISEEVAL_OEMRET_x64FRE_en-us.iso`,
  SHA-256 `bc3f24086ebadc94489066b5ad78089e2cf5c3491e90e790bb81a2b199c10e38`.
  Its `boot.wim` is build 26100.9457, its `install.wim` is build 26300 and
  7.3 GB large, with one image.
- Network: a bridge in its own network namespace with no uplink.

## Booting Windows PE

wimboot was given only `boot.wim` and the injected files:

```
#!ipxe
kernel http://192.168.77.1/wimboot
initrd http://192.168.77.1/inject/0/kyro-start.cmd kyro-start.cmd
initrd http://192.168.77.1/inject/0/winpeshl.ini winpeshl.ini
initrd http://192.168.77.1/iso/sources/boot.wim boot.wim
boot
```

- It boots on UEFI. wimboot takes the boot manager, `BCD` and `boot.sdi` out
  of the WIM by itself. Passing them from the ISO is not needed.
- The injected `winpeshl.ini` replaces Setup with the injected script.
- Between 30 and 60 seconds pass from the firmware's DHCP request to the
  script's first line. Most of that is loading the 612 MB `boot.wim`.

What the stock image contains matters for the start script:

- Present: `net`, `reg`, `wpeutil`, `drvload`, `dism`, `diskpart`, `bcdedit`,
  `xcopy`, `robocopy`, `netsh`, `ping`, `ftp`.
- Absent: `curl`, `tar`, `powershell`, `sc`, `where`, `iscsicli`, `certutil`.
  A start script can only be a batch file, and it has no HTTP client.

## SMB access from Windows PE

| Test | Result |
| ---- | ------ |
| Named account, server requires signing | Works. Session is SMB 3.1.1, signed with AES-128-GMAC. |
| Guest, no credentials, server allows it | Works. This Windows PE does not block guest access. |
| Named account, server does not require signing | Works. The client signs only part of the session. |
| `net use /TCPPORT:4445` | Works. The connection goes to port 4445. |

- Windows PE 26100 has `EnableSecuritySignature` set and
  `RequireSecuritySignature` not set. It neither requires signing nor blocks
  guest logons, unlike an installed Windows 11 Pro or Enterprise.
- The decision to use a named account and signing stays. It works here, it
  works on clients that do enforce signing, and it is one code path. The
  first logon command of the installed Windows 11 Enterprise also mapped the
  share with the named account.
- `net use /TCPPORT:` working in Windows PE means the embedded server does
  not have to listen on port 445 for clients of this Windows version. That
  would remove both the privileged port and the conflict with Samba. It is
  only verified for this one build.

### Half-open sessions delay the next boot

During the spike, mapping the share sometimes took 50 to 160 seconds. The
cause was the harness, but the effect is real and will hit Kyro:

- A client that is reset while it has an SMB session leaves the TCP
  connection open on the server.
- Windows PE picks the same source ports after every boot. The next boot's
  first connection attempts hit the stale connections and get no answer.
  Each attempt takes about 21 seconds to fail.
- After a failure, Windows caches the result. Retries within the next 30
  seconds fail immediately without sending a packet.
- With the stale connections removed, the share maps 0.1 seconds after the
  network is up, on the first attempt.

Consequences for Kyro: the embedded server needs short TCP keepalives and
should drop a client's old sessions when it sees that client boot again. The
start script needs a patient retry loop in any case.

## Starting Setup

Which Setup front end starts depends only on which `setup.exe` is run:

| Command | Front end | Finds the image |
| ------- | --------- | --------------- |
| `\\server\share\setup.exe` | New ("Windows 11 Setup") | Yes |
| `\\server\share\sources\setup.exe` | Previous | Yes |
| The "Previous Version of Setup" link in the new front end | Previous | Yes |
| `X:\sources\setup.exe /installfrom:\\server\share\sources\install.wim` | Previous | Yes |
| `X:\setup.exe /installfrom:...` | Previous | Yes |
| `X:\setup.exe` without arguments | New | No: "A media driver your computer needs is missing" |

The last row is the problem ADR 0001 set out to solve. The new front end
cannot be told where the image is. It only works when it is run from a
complete copy of the media.

What was run through to the end:

| Start | Front end | Mode | Result |
| ----- | --------- | ---- | ------ |
| From the share root | New | Interactive | Installed, reached the out-of-box experience |
| From the share root | New | Unattended | Installed, logged on, first logon command ran |
| From RAM with `/installfrom` | Previous | Unattended | Installed, logged on, first logon command ran |
| From the share, previous front end | Previous | Interactive | Reached disk selection; not installed to the end |

Each installation was run once. An unattended installation took 35 to 45
minutes from power-on to the desktop, with up to four virtual machines
sharing the host, so the times say nothing about Kyro.

### Setup from RAM ignores files on the media

Started from `X:\sources`, Setup looks for `ei.cfg` and `pid.txt` in
`X:\sources`, not next to the image. The evaluation ISO has an `EI.CFG` that
names the edition and the channel. Without it Setup asked for a product key
and then failed with "Windows cannot find the Microsoft Software License
Terms".

Copying `EI.CFG` from the share to `X:\sources\ei.cfg` before starting Setup
fixed it. The same will apply to `pid.txt` and probably to `$OEM$` folders,
which were not tested.

### What Setup asks of the SMB server

Captured during the two unattended installs:

| SMB2 request | From RAM with `/installfrom` | From the share root |
| ------------ | ---------------------------- | ------------------- |
| READ | 54,544 | 66,003 |
| CREATE | 14 | 3,830 |
| Distinct files opened | 5 | 1,253 |
| CLOSE | 13 | 3,267 |
| QUERY_INFO | 8 | 2,883 |
| QUERY_DIRECTORY | 4 | 222 |
| IOCTL | 9 | 113 |
| OPLOCK_BREAK | 0 | 27 |
| LOCK | 0 | 2 |

- From RAM, Setup opens the image and little else. Most reads are 4 to 24 KB.
- From the share root, Setup first copies the whole media to the disk. It
  opens 1,253 files, requests leases on 1,492 opens, loads executables over
  SMB, takes byte-range locks, and lists directories with two information
  classes (37 and 81).

For an embedded server the first column is a far smaller target than the
second.

## Answer files

`/unattend:X:\Windows\System32\autounattend.xml`, with the file injected by
wimboot, was honoured by both front ends:

- The `windowsPE` pass ran: disk layout, image selection, licence
  acceptance, and a command that wrote a marker file to a share.
- The `oobeSystem` pass ran in the installed system: account creation,
  automatic logon, and a first logon command that wrote the installed
  Windows version (10.0.26300.9457) to a share.

Setup carries the file from Windows PE into the installed system by itself.
Kyro only has to deliver it once.

## Hardware requirement checks

Tested on a virtual machine without a TPM.

| Start | Front end | LabConfig set by the start script | Result |
| ----- | --------- | --------------------------------- | ------ |
| From the share root | New | No | Blocked: "The PC must support TPM 2.0" |
| From the share root | New | Yes | Reaches disk selection |
| From the share, previous front end | Previous | Yes | Reaches disk selection |
| From RAM with `/installfrom` | Previous | Yes | Reaches disk selection |

The start script set the five values with `reg add` before starting Setup.
No answer file was involved. The previous front end was not tested without
the bypass, and no bypassed installation was run to the end.

With OVMF's Secure Boot capable firmware and Secure Boot not enabled, Setup
did not complain about Secure Boot.

## iSCSI

`net start msiscsi` answers "The service name is invalid" in the stock Setup
image. The driver file `msiscsi.sys` is in the image, but the service is not
registered and `iscsicli.exe` is absent. Image 1 of `boot.wim` contains the
same files; only image 2 was booted.

Option E of ADR 0001 therefore does not work with an unmodified `boot.wim`.
Whether Windows PE would attach an iSCSI disk announced through iBFT was not
tested.

## Embedded server candidates

Four Go libraries with an SMB server were tried. Each got a small program
that serves the mounted ISO read-only with one account and signing required.
See [smbcand](../../scripts/spike-install-source/smbcand).

| Library | Version | Licence | Windows PE can map the share | Notes |
| ------- | ------- | ------- | ---------------------------- | ----- |
| `github.com/jfjallid/go-smb` | v0.12.0 | MIT | Yes | |
| `github.com/sirrobot01/facetfs` | v0.7.0 | MIT | Yes | Names are matched case-sensitively |
| `github.com/sonroyaalmerol/go-smb-server` | v0.2.0 | MIT | No | "The specified network name is no longer available" |
| `github.com/absfs/smbfs` | v0.9.1 | MIT | No | System error 87 during session setup |

For the two that connect:

| Test | `jfjallid/go-smb` | `sirrobot01/facetfs` |
| ---- | ----------------- | -------------------- |
| Copy the 612 MB `boot.wim` back and compare | Identical | Identical |
| `dir sources\install.*` | "File Not Found" | "File Not Found" |
| Unattended install, Setup from RAM | Installed, first logon command ran | Installed, first logon command ran (after the `EI.CFG` change below) |
| Unattended install, Setup from the share root | Installed, first logon command ran | Fails at once: "The file 'autorun.dll' could not be loaded or is corrupt", error 0x7E |

- Both fail the wildcard listing that Samba answers. Windows sends its own
  wildcard characters for patterns like `install.*`, and neither library
  implements them.
- `facetfs` did not find `ei.cfg` because the file is called `EI.CFG`. The
  harness now asks for the exact name. A server for Windows clients has to
  match names without regard to case.
- The `autorun.dll` failure of `facetfs` was not investigated. Error 0x7E
  means that a module was not found, which would fit the same cause.
- `jfjallid/go-smb` is the only candidate that passed everything that Samba
  passed, apart from the wildcard listing. Each of its installs was run
  once.
- `go-smb-server` and `facetfs` only document tests against Linux clients.
  All four work with `smbclient` to some degree, which says little about
  Windows.

Size and dependencies of the two that connect:

| | `jfjallid/go-smb` | `sirrobot01/facetfs` |
| --- | --- | --- |
| Server package, lines without tests | 8,749 | 3,533 |
| Test files in the server package | 33 | 6 |
| Fuzz tests | None | 1 |
| Modules pulled into the binary | 8 more, among them forks of the Go ASN.1 package and of a Kerberos library | `golang.org/x/crypto` |
| SMB encryption | Yes | No |

The choice between one of these and a server of our own belongs in its own
decision record.

## What changes in the design

1. **Start Setup from RAM by default.** It needs five files from the server
   instead of 1,253, and no leases, locks or executable loading. The cost is
   the previous front end and having to copy `ei.cfg` and `pid.txt`. Running
   Setup from the share remains possible where the server can take it.
2. **Serve only what is needed.** With Setup started from RAM the share needs
   `install.wim` and the few configuration files in `sources`.
3. **Name lookups must ignore case**, in the SMB server and in Kyro's own
   reading of the ISO.
4. **Session hygiene.** Short keepalives, and old sessions of a client are
   dropped when it boots again.
5. **Alternative port.** `/TCPPORT:` is worth offering as an option. Port 445
   stays the default until more Windows versions are tested.
6. **The start script is a batch file.** There is no PowerShell and no HTTP
   client in the image.
7. **iSCSI is not a fallback** for an unmodified `boot.wim`.
8. **The answer file is delivered once**, through wimboot, and Setup does the
   rest.

## Not tested

- Secure Boot enabled, and the signed iPXE shim.
- Legacy BIOS clients.
- Any Windows release other than this one ISO.
- Real hardware, and network cards without a driver in `boot.wim`. Driver
  injection with `drvload` was not tried.
- `install.esd`, images with several editions, split images.
- `pid.txt` and `$OEM$` folders with Setup started from RAM.
- A complete installation with the hardware requirement bypass.
- Several clients installing at the same time, and throughput.
- Windows as the host.

## Notes on how the ISO was used

- The Evaluation Center page asks visitors to register before downloading.
  The spike used the direct download link that the page contains.
- Completing an installation means accepting Microsoft's evaluation licence
  terms in Setup. That was done in the test virtual machines.
- The ISO and the virtual disks were kept outside the repository and are not
  part of it.
