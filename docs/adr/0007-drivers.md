# ADR 0007: Drivers for Windows PE and the installed system

- **Status:** Accepted on 2026-11-16.
- **Date:** 2026-11-16
- **Deciders:** Kyro maintainers

## Context

Kyro starts Windows PE out of the `boot.wim` of the Windows ISO, unchanged
([ADR 0001](0001-windows-install-source.md)). That Windows PE has the
drivers Microsoft ships. On hardware it has no driver for, the installation
stops at one of two places:

- **The network card.** The start script cannot connect the install share,
  and Setup never starts.
- **The disk controller.** Setup starts and finds no disk. The common case
  today is Intel VMD, which many laptops and desktops with Intel processors
  of the 11th generation and later have switched on in firmware.

The usual answer is to add the drivers to `boot.wim` with DISM. Kyro does
not change Microsoft's files, and DISM is not there on a Linux server.

What there is to work with, checked rather than assumed:

- **wimboot** (v2.9.0, read in its source) puts extra files into
  `X:\Windows\System32` of Windows PE. It takes at most 63 files in all,
  their names can have 31 characters, and directories are lost. A driver
  package has several files, often in directories, and two packages can
  have files of the same name. So the drivers have to travel as **one
  file** and be unpacked in Windows PE.
- **Windows PE** (the one in the Windows 11 ISO, listed with wimlib) has
  `expand.exe` and `drvload.exe`. It has no `tar`, no `curl` and no
  PowerShell. `expand` unpacks cabinet files.
- **Windows Setup** looks for a directory called `$WinPEDriver$` at the
  root of every drive and adds the drivers in it to the system it installs.
  Microsoft's article "Limitations of $WinPeDriver$" describes the
  combination Kyro uses: load a driver in Windows PE with `drvload` from
  that directory, and the same driver is carried into the installed system.

## Decision

### Packs

A **driver pack** is a directory with `.inf` files and the files they name,
as the maker of the hardware ships it. `kyro driver add <directory>` copies
it into `<data_dir>/drivers/<name>/`. Kyro does not look into the files
beyond finding the `.inf` files, and changes nothing in them.

**Every installation is given all packs.** Windows PE loads each driver;
one that matches no device of the machine does nothing. Choosing packs per
client or per image is left for later: it needs a way to say which machine
has which hardware, and loading a driver that is not needed costs a few
megabytes of memory for the length of the installation.

Limits: 100 MB and 2000 files for all packs together, because Windows PE
holds them in memory several times (the cabinet, the unpacked files, and
its driver store). Paths are limited to what Windows PE can unpack:
printable ASCII, no characters that Windows forbids, 180 characters.

### How the drivers reach the machine

1. When a client is given the installer, the iPXE script gets one more
   line, and iPXE fetches `/i/<token>/kyrodrv.cab`: all packs in one
   cabinet, each in a directory of its name. The cabinet is written on the
   fly, uncompressed, by a small writer of Kyro's own (`internal/cab`).
2. wimboot puts it into `X:\Windows\System32\kyrodrv.cab`.
3. The start script, after `wpeinit` and before it waits for the network:

   ```bat
   mkdir "X:\$WinPEDriver$"
   expand X:\Windows\System32\kyrodrv.cab -F:* "X:\$WinPEDriver$"
   for /r "X:\$WinPEDriver$" %%i in (*.inf) do drvload "%%i"
   wpeutil InitializeNetwork
   ```

4. The share is connected and Setup starts as before. Setup finds
   `X:\$WinPEDriver$` by itself and adds the drivers to the installed
   system. No answer file has to know about any of this.

Without packs none of this happens: the iPXE script has no extra line, and
the start script skips the block because the cabinet is not there.

What was handed out must be what was added. If a file of a pack is missing
or has another size than when the pack was added, the pack is not handed
out, the installation does not start and the client is marked `failed`
with the reason. `kyro doctor` reports such a pack.

### Fetching well-known packs

`kyro driver fetch <name>` downloads a pack from its maker and stores it.
Like iPXE and wimboot ([ADR 0001](0001-windows-install-source.md)), these
files are never part of Kyro's repository or releases. The versions are
fixed, and so is the SHA-256 checksum of every file; a download that does
not match is not stored.

| Name | What | Maker's license |
| ---- | ---- | --------------- |
| `intel-rst` | Intel RST (VMD) storage driver 20.4.0.1006, for Intel platforms of the 12th to 15th generation | Intel Software License Agreement; must be accepted |
| `intel-rst-11` | The same driver in version 18.7.6.1010, for the 11th generation | Intel Software License Agreement; must be accepted |
| `virtio` | Network, SCSI and block drivers of virtio-win 0.1.302, for virtual machines | BSD 3-Clause |

- **Intel's license.** Intel lets nobody download these files without
  accepting its license agreement. Kyro therefore fetches them only for an
  administrator who states that they accept it: `kyro driver fetch
  intel-rst --accept-license`. Without the statement nothing is requested
  from Intel's server. `install.sh` asks, with the address of the license
  and "yes" as the default answer; without a terminal it needs
  `--accept-intel-license` and otherwise skips the driver and says how to
  get it.
- **Intel's installer.** Intel ships the driver inside `SetupRST.exe`. The
  driver files are in ZIP archives inside that program. Kyro downloads the
  program, checks its checksum, takes the files out and checks each of
  them. It does not run the program.
- **The two Intel packs exclude each other.** Both carry a driver called
  `iaStorVD` in different versions, and Microsoft's advice is not to mix
  versions of one driver during an installation. `kyro driver fetch`
  refuses one while the other is stored. A site with machines of both
  kinds has to switch packs until Kyro can choose packs per client.
- **virtio.** The drivers are in an ISO image of 836 MB. Kyro reads only
  the sectors that hold the eleven files it wants, with HTTP range
  requests and a small ISO 9660 reader of its own (`internal/iso9660`).

## What was tested, and what was not

In a virtual machine (QEMU, Secure Boot enforced) with a **virtio network
card and a virtio SCSI controller**, hardware for which Windows has no
driver, and with the packs `virtio` and `intel-rst`:

- Windows PE unpacked the cabinet with its directories and loaded all three
  drivers (`netkvm.inf`, `vioscsi.inf`, `iaStorVD.inf`).
- The share was connected through the virtio network card, Setup found the
  disk behind the virtio controller, and the installation ran to the end.
- The installed Windows started from that disk and called back through
  that network card, so both drivers were in the installed system.
- The driver store of the installed Windows, read from its disk, has all
  three drivers, the Intel one included.
- The same machine without any pack, as a check that the test means
  something: Windows PE could not reach the share ("The network location
  cannot be reached"), and the installation stopped there.

That was one full run of the end-to-end test (27 minutes) and one start
without packs. `install.sh` was run once from a terminal in a container
with the default answers: it showed Intel's license, downloaded the driver
from Intel and stored it.

To try packs on a machine without installing it, there is a built-in
profile that leaves every question to Setup's screens (`kyro profile import
--example-interactive`): Windows PE loads the packs and Setup's disk page
shows which disks it sees. In the virtual machine above it listed the disk
behind the virtio controller.

**Not tested: Intel VMD hardware.** QEMU has no such controller. What is
shown for the Intel driver is that Windows PE accepts and loads the
package and that it arrives in the installed system. That it makes a disk
behind a real VMD controller visible to Setup is what the driver is made
for, and is untested here. The pack for the 11th generation was fetched and
stored, and never loaded anywhere.

## What this costs and does not do

- **A driver is code that runs with full rights**, in Windows PE and in the
  installed Windows of every machine. Kyro copies what it is given. For
  packs it fetches, it checks that the files are the ones it pins; it does
  not judge them, and it does not check signatures. Windows PE and Windows
  do: an unsigned driver is refused there, not by Kyro.
- Whoever can write `<data_dir>/drivers` decides what runs on every
  installed machine. The directory belongs to root, like the profiles.
- A stored pack is checked by the sizes of its files, not by checksums,
  when it is handed out.
- Packs are for x86-64 Windows 11. Kyro does not check that a pack fits.
- Drivers that only the installed system needs (graphics, audio) work the
  same way but are not what this is for; Windows Update usually brings
  them, and they count against the 100 MB.
- Updating a pinned source means a new Kyro release, as with iPXE.
