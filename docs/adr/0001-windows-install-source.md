# ADR 0001: How Windows Setup gets its install source

- **Status:** Proposed. Awaiting maintainer approval; nothing here is implemented.
- **Date:** 2026-11-12
- **Deciders:** Kyro maintainers

> **Evidence level.** This ADR is desk research: upstream documentation, issue
> trackers and community write-ups, listed under [References](#references).
> Kyro has not booted Windows yet, so no statement below has been verified by
> the project. Claims that the recommendation depends on are collected in the
> [validation spike](#validation-spike) and must be confirmed there before the
> status changes to Accepted.

## Context

Kyro's planned boot chain is proxyDHCP, TFTP or HTTP, iPXE, wimboot, Windows
PE. wimboot gets the client as far as a running Windows PE (WinPE): it loads
`boot.wim` and the boot files over HTTP into RAM and starts the Windows boot
manager from a virtual disk.

That virtual disk only exists while the firmware is in control. Once the
Windows kernel runs, the client has a RAM-backed `X:` drive with the contents
of `boot.wim` and nothing else. On Windows install media, image 2 of
`boot.wim` is the Windows Setup environment. It contains `setup.exe`, but the
operating system image itself (`sources\install.wim` or `install.esd`, usually
4 to 7 GB) is a separate file on the media. Setup started from a bare
network-booted `boot.wim` cannot find it and stops.

So the question is: how does WinPE get file-level access to the install image
that sits inside an ISO on the Kyro server?

What a stock `boot.wim` from Windows 11 install media gives us to work with:

- An SMB client (`net use`). This is the mechanism documented by iPXE and
  netboot.xyz for network installs.
- No HTTP-backed file system, no WebDAV client, no NFS client.
- An iSCSI initiator driver, but reports disagree on whether the initiator
  service starts in current WinPE without adding optional components.
- No general-purpose RAM disk besides `X:`, whose writable space is small.
- Kernel drivers only load if they are signed in a way Windows accepts.

One wimboot feature shapes all options: wimboot can inject extra files into
`\Windows\System32` of the booted image on the fly (since v2.6.0). The WIM on
the server stays untouched. A `winpeshl.ini` injected this way replaces the
default shell, so Kyro can run its own start script instead of plain Setup
without modifying any Microsoft file.

## Decision criteria

1. **Reliability** across Windows 11 releases (24H2, 25H2 and later) and
   across hardware.
2. **No Windows licenses or redistribution.** Kyro must work with only the
   user's ISO. It must not need the Windows ADK, a Windows machine, or any
   Microsoft file shipped with Kyro.
3. **Ease of deployment.** One static binary, `kyro serve`, minimal config.
4. **Legal cleanliness.** GPLv3-compatible code only, no circumvention of
   Windows security mechanisms, no modified Microsoft binaries.

## Options

### A. SMB share served by Kyro itself (embedded, read-only)

Kyro contains a small read-only SMB 2/3 server. An injected start script maps
the share and starts Setup against it.

- Reliability: uses the path that Setup has supported for decades and that
  upstream documents. The risk moves into our SMB server: Windows is a strict
  client. Windows 11 24H2 Pro, Enterprise and Education require SMB signing
  by default, and required signing rules out guest access. Whether WinPE
  applies the same default is not documented, so the server must offer NTLMv2
  authentication with a real account and signing from day one. Then it works
  either way.
- Licenses: nothing from Microsoft is needed besides the user's ISO.
- Deployment: stays a single binary. Needs TCP 445, a privileged port, and
  conflicts with a Samba already running on the same address.
- Legal: clean. Candidate libraries are MIT-licensed, or we write the subset
  ourselves from Microsoft's open protocol specifications.

### B. SMB share served by an external Samba

Same client side as A, but the user installs and configures Samba, or Kyro
generates a config for it.

- Reliability: best of all options. Samba is the most tested non-Microsoft
  SMB server. Known pitfalls are configuration ones: guest shares fail where
  signing is required, and files need execute permission if Setup runs from
  the share.
- Licenses: none needed.
- Deployment: breaks the single-binary goal. Two daemons, distro-specific
  packaging, extracted ISO trees or loop mounts on disk.
- Legal: clean (Samba is GPLv3, used as a separate program).

### C. HTTP download by a WinPE-side script or agent

A script or a small Kyro-built Windows program downloads the install image
over HTTP and hands Setup a local path.

- Reliability: the download needs somewhere to go. RAM is not an option on
  stock WinPE (no RAM disk, and 4 to 7 GB would exclude many machines). The
  target disk is the only other place, which means Kyro has to partition a
  disk before the user has chosen one in Setup, and has to clean up the
  leftover image afterwards. That is destructive automation in the default
  path and collides with both interactive disk selection and user-supplied
  `autounattend.xml` disk configuration.
- Licenses: none needed.
- Deployment: single binary (the agent would be cross-compiled and embedded).
- Legal: clean.

### D. HTTP-backed virtual disk through a kernel driver (the iVentoy approach)

A driver in WinPE presents the ISO on the server as a local CD drive, backed
by HTTP range requests. Setup behaves exactly as if booted from DVD.

- Reliability: good once the driver loads, and that is the problem. Windows
  only loads kernel drivers signed through Microsoft's hardware program when
  Secure Boot is on. iVentoy's developer stated that it boots WinPE in test
  signing mode to load an unsigned `httpdisk` driver, and in May 2025 it was
  reported to also install a self-signed root certificate for a "signed"
  variant of that driver. Test signing mode cannot be enabled with Secure
  Boot on.
- Licenses: an open-source project has no realistic way to get and keep a
  Microsoft-attested kernel driver signature.
- Deployment: single binary, but needs BCD changes and a shipped driver.
- Legal: weakening driver signature enforcement to load our own kernel code
  is exactly what a transparent, auditable tool should not do. Rejected.

### E. iSCSI: the ISO as a read-only CD-ROM LUN

Kyro contains an iSCSI target. iPXE attaches it with `sanhook`, which writes
an iBFT table; WinPE's iSCSI stack reconnects and shows a DVD drive.

- Reliability: mixed evidence. One 2025 write-up reports exactly this working
  for Windows 11 (with an ADK-built WinPE and injected NIC drivers). Other
  reports say the WinPE iSCSI service has not started without extra optional
  components since Windows 10 2004, and the iPXE tracker shows Windows 11
  iSCSI handover failing depending on NIC driver and Windows build.
- Licenses: none needed if stock `boot.wim` is sufficient, which is the open
  point. If optional components are required, the user needs the ADK.
- Deployment: single binary. A read-only target is less code than an SMB
  server.
- Legal: clean.

### F. Modifying `boot.wim` (offline servicing)

Kyro rewrites the user's `boot.wim` to add scripts, drivers or packages.

- wimboot's on-the-fly injection already covers scripts, answer files and
  loose driver files without touching the WIM, so modification buys nothing
  for those.
- It would only be needed for things that must be present inside the image,
  such as WinPE optional components. Those come from the ADK, which Kyro can
  neither ship nor expect users to have.
- Modifying the user's own copy locally is not redistribution, but it adds a
  WIM writer and registry hive editing to Kyro and makes failures harder to
  attribute. Not needed, so not done.

### G. Replacing Setup: Kyro applies the image itself

A WinPE agent partitions the disk and applies `install.wim` directly from
HTTP, then runs `bcdboot`.

- This is a different product (an imaging tool). It loses Setup's UI, edition
  selection and `windowsPE` answer file handling, needs a faithful WIM and
  ESD implementation, and puts disk wiping into Kyro. Out of scope.

### Comparison

| Option | Reliability | No MS licenses or files | Single binary | Legally clean |
| ------ | ----------- | ----------------------- | ------------- | ------------- |
| A. Embedded SMB | Good, pending our server's Windows interop | Yes | Yes | Yes |
| B. External Samba | Best | Yes | No | Yes |
| C. HTTP download | Poor (needs disk before Setup) | Yes | Yes | Yes |
| D. HTTP disk driver | Good only without Secure Boot | Yes | Yes | No |
| E. iSCSI CD-ROM | Uncertain on stock WinPE | Uncertain | Yes | Yes |
| F. Modify `boot.wim` | Not needed for A or B | Risk of ADK dependency | Yes | Yes |
| G. Replace Setup | Out of scope | Yes | Yes | Yes |

## Recommendation

**Adopt A, an embedded read-only SMB server, as the default, and support B,
an external SMB server, as a configuration choice.** Never modify `boot.wim`
(F). Reject D. Keep E as a documented fallback candidate if the spike shows
that an embedded SMB server cannot be made reliable against Windows clients.

SMB is the only file access that stock WinPE offers without a kernel driver,
without the ADK and without touching the disk early. A and B share the entire
client side, so supporting both costs one config switch:

```yaml
smb:
  mode: embedded   # embedded | external | off
```

`external` means Kyro still generates the start script but points it at a
share the user runs. That covers hosts where port 445 is taken and gives an
escape hatch while the embedded server is young.

### Planned design

Everything below is a plan to be confirmed in Phase 2.

- **Over HTTP, through wimboot:** `boot.wim` from the ISO plus injected
  files. wimboot can extract the boot manager and `BCD` from the WIM itself;
  whether Kyro also passes them from the ISO explicitly is a Phase 2 detail.
- **Injected files** (generated by Kyro, placed in `X:\Windows\System32` by
  wimboot): `winpeshl.ini`, a start script, and optionally the image's
  `autounattend.xml` and loose NIC driver files.
- **Start script:** run `wpeinit`, wait for the network, load injected
  drivers with `drvload`, map the share with retries, then start Setup and
  pass the answer file explicitly with `/unattend:`.
- **How Setup is started** is the main thing the spike has to settle. Two
  candidates:
  1. `X:\sources\setup.exe /installfrom:\\kyro\<image>\sources\install.wim`.
     Setup runs from RAM and reads one large file over SMB. Smallest SMB
     surface, so the preferred candidate for an embedded server.
  2. `\\kyro\<image>\setup.exe`, the way upstream documents it. Closest to
     booting the DVD, which matters for custom ISOs that carry `$OEM$`
     folders or `ei.cfg`, but it loads many executables over SMB.

  Windows 11 24H2 introduced a new Setup front end, and several reports
  describe network installs that only work with the previous one. The spike
  must test both front ends.
- **SMB server scope:** SMB 2.1 to 3.1.1, NTLMv2 with a non-guest account,
  signing, read-only, no SMB1, no guest, bound to the boot interface only.
  The credentials are in a script served over plain HTTP, so they are not a
  secret. They exist because signing needs a real session. Anyone on the boot
  network can read the ISO contents, which is already true for the HTTP side.
- **Serving from the ISO:** files are read from the ISO by offset (Windows
  ISOs are UDF), so nothing has to be unpacked to disk. The share exposes the
  ISO tree read-only.
- **Implementation:** evaluate the MIT-licensed `jfjallid/go-smb` server
  package and `sonroyaalmerol/go-smb-server` against a minimal implementation
  of our own. Neither documents testing against Windows clients. AGPL
  libraries are excluded. The choice gets its own ADR after the spike.

### Consequences

- Kyro has to listen on TCP 445. That needs `CAP_NET_BIND_SERVICE` like the
  other privileged ports and fails if Samba already owns the address.
  Windows 11 24H2 added `net use /TCPPORT:` for alternative ports; whether
  WinPE's `net use` supports it is unverified.
- An SMB server is security-sensitive parsing code on a well-known port.
  Read-only, interface-bound, fuzz-tested, and possible to switch off.
- WinPE needs a working NIC driver. Stock `boot.wim` lacks drivers for some
  current NICs. This affects every network option equally. Mitigation is a
  per-image driver directory injected by wimboot and loaded with `drvload`.
- The client loads all of `boot.wim` into RAM, on top of what Windows 11
  itself requires.

## Validation spike

To be done at the start of Phase 2, in QEMU with OVMF, before building the
image pipeline. It needs a Windows 11 ISO, which the maintainer has to
supply or approve downloading for local testing.

1. Stock Windows 11 25H2 `boot.wim` boots via wimboot with an injected
   `winpeshl.ini` and script, and the script runs.
2. From that WinPE, `net use` against a signing-required Samba share works
   with a named account. Record what happens with a guest share.
3. Both ways of starting Setup reach the disk selection page and complete an
   install, with the new and the previous Setup front end.
4. Repeat 2 and 3 against each embedded SMB server candidate.
5. `/unattend:` with an injected answer file is honoured.
6. Check whether `net start msiscsi` works in stock WinPE (decides whether E
   stays a fallback candidate).

The recommendation holds if 1 to 3 pass. If 4 fails for all candidates and a
minimal server of our own is not feasible, Kyro ships with B only and E is
re-evaluated.

## Windows 11 requirements and PXE booting

Windows 11 requires UEFI, a Secure Boot capable system, and TPM 2.0, besides
CPU, RAM and storage minimums. Setup checks these inside WinPE before it
installs. None of them is checked by the PXE boot chain itself, but they
constrain it.

- **UEFI.** Windows 11 does not install in legacy BIOS mode without a
  bypass. UEFI x86-64 is therefore the primary path. BIOS PXE support is
  still useful for virtual machines and bypass installs, and Phase 1 keeps
  it.
- **Secure Boot.** The requirement is that the machine is capable of Secure
  Boot, not that it is enabled. With Secure Boot off, any iPXE build works.
  With Secure Boot on, the firmware only starts binaries in a trusted chain:
  - iPXE 2.0.0 (March 2026) ships official signed binaries: a
    Microsoft-signed shim (`ipxe-shimx64.efi`) that loads an `ipxe.efi`
    signed by the iPXE project's own CA. Only release builds are signed.
  - A self-built iPXE is not trusted. Kyro therefore cannot rely on an
    embedded iPXE script. It has to use stock iPXE and hand over its script
    URL through DHCP, detecting iPXE by its user class to avoid a loop. This
    is a hard constraint for Phase 1.
  - Upstream states that wimboot works on UEFI systems with Secure Boot
    enabled. v2.9.0 (November 2025) added what Windows 11 24H2 and newer
    need to boot, and selects the 2023-signed Windows boot manager where the
    firmware trusts the 2023 certificate.
  - Microsoft's 2011 Secure Boot certificates expired between June and
    October 2026 and are being replaced by 2023 ones. Machines differ in
    which they trust. The iPXE shim was re-released with both signatures in
    May 2026; problems are still being reported on some platforms, Hyper-V
    among them.
  - Kyro's position: Secure Boot off works first. Secure Boot on is a
    separate roadmap item that is only marked working after it has been
    tested, and it depends on upstream's signed binaries, which Kyro fetches
    and never rebuilds.
- **TPM.** No effect on network booting. It is only checked by Setup.

### LabConfig bypass through `autounattend.xml`

Setup skips individual hardware checks if matching values exist under
`HKLM\SYSTEM\Setup\LabConfig` in the running WinPE: `BypassTPMCheck`,
`BypassSecureBootCheck`, `BypassRAMCheck`, `BypassCPUCheck` and
`BypassStorageCheck`, each a `REG_DWORD` set to 1. The key is undocumented.
Community reports say it still works for clean installs of 24H2 and 25H2.

An answer file can set the values, because Setup runs `RunSynchronous`
commands of the `windowsPE` pass before it evaluates the requirements:

```xml
<settings pass="windowsPE">
  <component name="Microsoft-Windows-Setup" processorArchitecture="amd64"
             publicKeyToken="31bf3856ad364e35" language="neutral"
             versionScope="nonSxS">
    <RunSynchronous>
      <RunSynchronousCommand wcm:action="add">
        <Order>1</Order>
        <Path>reg add HKLM\SYSTEM\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f</Path>
      </RunSynchronousCommand>
      <!-- one command per check -->
    </RunSynchronous>
  </component>
</settings>
```

Kyro has a simpler second route: its start script runs before Setup, so it
can execute the same `reg add` commands itself, without editing the user's
answer file.

How Kyro would offer this, if at all:

- Off by default, opt-in per image, one switch per check.
- Kyro injects nothing unless asked. A user's own `autounattend.xml` that
  already contains the commands is passed through unchanged either way.
- Documented with a clear warning: Microsoft does not support Windows 11 on
  hardware below the requirements and states such devices are not entitled
  to updates. The mechanism can stop working with any release.
- It does not touch activation or licensing. It is a registry setting in the
  user's own installer session, and no Microsoft file is modified.

Whether Kyro ships a built-in switch for this is a maintainer decision and
is listed below.

## Open questions for the maintainer

1. Approve A as default with B as a config option?
2. Should Kyro offer the LabConfig bypass as a built-in opt-in switch, or
   only pass through whatever the user's own `autounattend.xml` contains?
3. Windows 11 ISO for the spike: supplied by the maintainer, or may the
   Microsoft evaluation ISO be downloaded for local testing (never
   committed)?

## References

- iPXE, "Network-booting Windows PE": <https://ipxe.org/howto/winpe>
- iPXE, "wimboot": <https://ipxe.org/wimboot>
- Microsoft, "Windows Secure Boot certificate expiration and CA updates":
  <https://support.microsoft.com/en-us/topic/windows-secure-boot-certificate-expiration-and-ca-updates-7ff40d33-95dc-4c3c-8725-a9b95457578e>
- wimboot releases: <https://github.com/ipxe/wimboot/releases>
- netboot.xyz, "Installing Windows via PXE":
  <https://netboot.xyz/docs/kb/pxe/windows/>
- Microsoft, "Control SMB signing behavior":
  <https://learn.microsoft.com/en-us/windows-server/storage/file-server/smb-signing>
- Microsoft, "SMB alternative ports now supported in Windows Insider":
  <https://techcommunity.microsoft.com/blog/filecab/smb-alternative-ports-now-supported-in-windows-insider/3974509>
- Born, "Warning: iVentoy 1.0.2 installs obscure certificates and drivers":
  <https://borncity.com/win/2025/05/07/warning-iventoy-1-0-2-installs-obscure-certificates-and-drivers>
  and the underlying report <https://github.com/ventoy/PXE/issues/106>
- T. Stock, "Netboot Windows 11 with iSCSI and iPXE":
  <https://terinstock.com/post/2025/02/Netboot-Windows-11-with-iSCSI-and-iPXE/>
- NTLite forum, "Workaround for Win 11 WinPE iSCSI initiator service not
  starting":
  <https://ntlite.com/community/threads/workaround-for-win-11-winpe-iscsi-initiator-service-not-starting.3705/>
- iPXE discussion 1544, "Windows 11 working version":
  <https://github.com/ipxe/ipxe/discussions/1544>
- iPXE discussion 1814, shim signatures and the 2023 certificates:
  <https://github.com/ipxe/ipxe/discussions/1814>
- iPXE shim: <https://github.com/ipxe/shim>
- wimboot discussion 64, Hyper-V with Secure Boot:
  <https://github.com/ipxe/wimboot/discussions/64>
- Javen881/PXE-NetBoot, notes on Samba permissions and `/InstallFrom`:
  <https://github.com/Javen881/PXE-NetBoot>
- Elevenforum, unattended installation on unsupported hardware:
  <https://www.elevenforum.com/t/how-to-perform-an-unattended-installation-of-windows-11-on-unsupported-hardware.1981/>
- SMB server candidates: <https://github.com/jfjallid/go-smb>,
  <https://github.com/sonroyaalmerol/go-smb-server>
