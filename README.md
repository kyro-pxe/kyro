<!-- Logo placeholder: there is no logo yet. Replace this comment with the image once one exists. -->

<h1 align="center">Kyro</h1>

<p align="center">
  An open-source PXE server that installs Windows 11 over the network, unattended.
</p>

<p align="center">
  <a href="https://github.com/kyro-pxe/kyro/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/kyro-pxe/kyro/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="LICENSE"><img alt="License: GPLv3" src="https://img.shields.io/badge/license-GPLv3-blue.svg"></a>
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/kyro-pxe/kyro"></a>
</p>

> [!WARNING]
> **Status: alpha.** Kyro has installed Windows 11 end to end in one
> environment only: virtual machines (QEMU, UEFI, with Secure Boot on and
> off) with the Windows 11 Enterprise evaluation ISO. It has not been tried
> on physical hardware. Kyro wipes the disks of the machines you arm, and anyone on the
> boot network can read what it serves. Read
> [Known limitations](#known-limitations) and
> [docs/security-model.md](docs/security-model.md) first.
> [docs/ROADMAP.md](docs/ROADMAP.md) has the honest status of every item.

## What it does

You give Kyro a Windows 11 ISO and an answer file (`autounattend.xml`), and
you **arm** a machine by its MAC address, UUID or serial number. The next
time that machine boots from the network, Kyro installs Windows on it
without anyone touching it. The installed system reports back, and
`kyro clients` shows it as `provisioned`.

- One static binary: proxyDHCP, TFTP, HTTP and a read-only SMB server in
  one process. It runs next to your existing DHCP server and changes nothing
  on it.
- ISOs are read in place. Nothing is unpacked or mounted.
- A machine is armed for **one** installation. Every later network boot is
  sent to the local disk, so a machine that always boots from the network
  is not reinstalled in a loop.
- A machine that is not armed never gets an installer, and an arming that
  is not used runs out (after 24 hours unless you say otherwise).
- Kyro does not generate answer files. You bring your own, and it fills in
  per-machine values: what the machine says about itself, such as
  `{{mac}}` or `{{serial}}`, and what you give when arming it, such as
  `{{var:hostname}}` with `kyro arm --set hostname=PC-014`.

Kyro contains no Microsoft software. You need your own Windows ISO and
licenses. iPXE and wimboot are separate programs (GPLv2) that Kyro downloads
unmodified from their projects at setup time; see [NOTICE](NOTICE).

## Quick start

You need a Linux server (x86-64, systemd) on the same network as the
machines, a DHCP server on that network, and a Windows 11 ISO. The client
needs UEFI and network boot first in its boot order. Secure Boot can stay
switched on: Kyro hands out the boot loaders that Microsoft and the iPXE
project signed ([ADR 0005](docs/adr/0005-secure-boot.md)).

**1. Install.** The installer verifies the release's checksum, creates a
`kyro` user, writes `/etc/kyro/config.yaml` and starts the service.
[docs/install.md](docs/install.md) has the details.

```sh
curl -fsSLO https://github.com/kyro-pxe/kyro/releases/download/v0.1.0-alpha/install.sh
curl -fsSLO https://github.com/kyro-pxe/kyro/releases/download/v0.1.0-alpha/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
sudo sh install.sh
```

**2. Check the machine.** `kyro doctor` looks at ports, conflicting
services, the firewall and disk space. It changes nothing; it prints the
commands for you to run.

```sh
sudo kyro doctor
```

**3. Add an image.**

```sh
sudo kyro image add /path/to/windows11.iso --name win11
```

**4. Add a profile.** Import your own answer file:

```sh
sudo kyro profile import /path/to/autounattend.xml --name office
```

Or, to try Kyro on a **test machine**, the built-in example. It is a test
fixture: it wipes disk 0, installs the first edition in the image and
creates an administrator with a public password. Do not use it on a machine
you care about.

```sh
sudo kyro profile import --example
```

There is a second built-in profile, `--example-interactive`, that answers
none of Setup's questions and so installs nothing by itself. It is for
looking at a machine before you install it; see
[Trying drivers without installing](#trying-drivers-without-installing).

Kyro shows the profile's warnings on import and again when you arm.

**5. Arm a client.** A profile that wipes a disk needs `--confirm-wipe`.

```sh
sudo kyro arm 52:54:00:12:34:56 --image win11 --profile kyro-example --confirm-wipe
```

**6. Boot the client from the network and watch.**

```sh
sudo kyro clients          # unknown, armed, installing, provisioned, failed, expired
journalctl -u kyro -f
```

The state goes from `armed` to `installing` when the client fetches the
installer, and to `provisioned` when the installed system calls back. In
Kyro's own test runs that took 24 to 36 minutes on a virtual machine. A client
that does not call back within `install_timeout` (3 hours) becomes `failed`.

If the answer file has places for values of the one machine, written
`{{var:NAME}}`, give them when arming:

```sh
sudo kyro arm 52:54:00:12:34:56 --image win11 --profile office \
    --set hostname=PC-014 --set product_key=XXXXX-XXXXX-XXXXX-XXXXX-XXXXX
```

Kyro refuses to arm without a value the profile needs, keeps the values
until the installation ends, and deletes them then.

While a client is `installing`, `kyro clients` says how far it has come:
whether Windows PE connected to the install share, whether Setup was
started, and how often Setup has restarted the machine. If Setup ends with
an error in Windows PE, the client becomes `failed` at once; if the time
runs out, the reason says where the installation stopped and what usually
causes that ([ADR 0008](docs/adr/0008-progress.md)).

The arming lasts 24 hours. If the client has not booted from the network by
then, it becomes `expired` and is not installed; arm it again. `kyro arm
--expires 2h` (or `7d`, up to `30d`) sets another time for one arming, and
`arm_timeout` in the configuration sets the default.

For `provisioned` to appear, the answer file has to make the installed
system call back. `kyro profile show kyro-example --xml` shows how the
example does it with one `FirstLogonCommands` entry and the token
`{{callback_url}}`.

A read-only status page is at `http://127.0.0.1:8080` on the server.

To take an arming back: `kyro disarm <client>`. To install the same machine
again: arm it again.

## Drivers

Windows PE has the drivers that Microsoft ships. If it has none for the
network card of a machine, the installation stops when it connects to the
install share; if it has none for the disk controller, Setup finds no disk.
The usual case is Intel VMD, which many machines with Intel processors of
the 11th generation and later have switched on.

Kyro hands driver packs to Windows PE, which loads them, and leaves them
where Windows Setup looks for drivers to add to the installed system. It
changes nothing in the Windows ISO.

```sh
sudo kyro driver fetch                                # what Kyro can download
sudo kyro driver fetch intel-rst --accept-license     # Intel VMD, 12th to 15th generation
sudo kyro driver fetch virtio                         # virtual machines (KVM, Proxmox)
sudo kyro driver add /path/to/unpacked/driver         # any other driver with .inf files
sudo kyro driver list
```

`kyro driver fetch` downloads fixed versions from the maker's own server
and checks every file against a checksum compiled into Kyro. Nothing of it
is part of Kyro. Intel lets nobody download its driver without accepting
the Intel Software License Agreement; `--accept-license` is your statement
that you have read and accept it, and `install.sh` asks the same question.

Every installation is given all stored packs. A driver is code that runs
with full rights on every machine it is loaded on: add drivers from the
maker of the hardware only. [ADR 0007](docs/adr/0007-drivers.md) has the
details and what was tested.

### Trying drivers without installing

To find out whether Windows PE gets a network and sees the disk of a
machine, without installing it, use the profile that leaves every question
to Setup's screens:

```sh
sudo kyro profile import --example-interactive
sudo kyro arm 52:54:00:12:34:56 --image win11 --profile kyro-example-interactive
```

Boot the machine from the network. What to look at:

- The black window at the start lists each driver that Windows PE loaded
  (`DrvLoad: Successfully loaded ...`) and says whether the install share
  was connected. If it was, the network card works.
- Windows Setup then opens and waits at its first page. Go on to "Where do
  you want to install Windows?": the disks listed there are the ones the
  storage drivers made visible. `Shift+F10` opens a command prompt
  (`diskpart`, then `list disk`).
- **Close Setup's window to end the test.** Kyro records the client as
  `failed` with "Windows Setup was closed at the machine", and the machine
  boots from its disk the next time.

Nothing is installed unless somebody picks a disk on that page and goes
on; from there it is an ordinary manual installation, and Setup deletes
what that person tells it to. The profile needs no `--confirm-wipe`
because Kyro itself changes no disk with it.

Windows PE is still a Windows that has started on the machine. It reads the
file systems it finds, as a Windows installation USB stick does. In Kyro's
test with a prepared disk the partition table, the FAT32 partition and all
files were the same afterwards, and 337 sectors inside the NTFS partition
had changed: what Windows writes into an NTFS volume when it opens it. On a
completely empty disk it wrote a four-byte disk signature.

## Known limitations

- **Tested in virtual machines only.** One ISO (Windows 11 Enterprise
  evaluation), QEMU with OVMF, ten full runs. No physical hardware, no other
  editions or languages.
- **Secure Boot works in virtual machines; real hardware is untried.** By
  default Kyro hands UEFI clients the shim and the iPXE that the iPXE
  project publishes signed. A machine that does not trust Microsoft's key
  for other vendors' boot loaders ("Microsoft 3rd Party UEFI CA", off by
  default on some business laptops) refuses them until that is allowed in
  its firmware setup. `secure_boot: false` goes back to the unsigned iPXE,
  which only boots with Secure Boot switched off. See
  [ADR 0005](docs/adr/0005-secure-boot.md).
- **UEFI x86-64 clients only.** BIOS clients are untested. No ARM.
- **Linux server only**, x86-64.
- **An existing DHCP server is required.** `dhcp.mode: full` is not
  implemented.
- **Drivers are tested in virtual machines only.** Kyro can give Windows PE
  and the installed system drivers that Windows lacks
  ([Drivers](#drivers)). That was tested with virtio hardware in a virtual
  machine. The Intel VMD driver was loaded there and arrived in the
  installed Windows, but no machine with an Intel VMD controller was
  tried.
- **Every installation gets every driver pack.** Packs cannot be chosen
  per machine yet, and the two Intel packs cannot be stored together.
- **No authentication of clients.** A machine is selected by the MAC
  address, UUID or serial number it claims. Another machine on the boot
  network can claim it and receives the installer and the answer file.
- **Everything is plain HTTP and SMB.** Anyone on the boot network can read
  the ISO contents and, during an installation, the answer file with all
  its passwords. Use an isolated boot network.
- **The callback is not authentication.** It marks progress. Whoever sees
  the token on the network can report "done".
- **No web interface.** The status page only shows state.
- **Release checksums only, no signatures.**
- **The profile bundle format has version 0** and may still change between
  releases. [ADR 0002](docs/adr/0002-profiles.md) is its specification.
- **Docker:** the Compose example was not tested with a booting client.
- The embedded SMB server uses a library in which fuzzing found defects;
  see [ADR 0003](docs/adr/0003-smb-server-library.md).

## Documentation

| Document | Content |
| -------- | ------- |
| [docs/install.md](docs/install.md) | Installer, files, the service's privileges, uninstalling |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Status of every item |
| [docs/architecture.md](docs/architecture.md) | How a boot works |
| [docs/security-model.md](docs/security-model.md) | What Kyro does and does not protect against |
| [docs/adr](docs/adr/README.md) | Design decisions, including the profile format |
| [docs/ALPHA.md](docs/ALPHA.md) | Plan and acceptance tests of this alpha |
| [docs/releasing.md](docs/releasing.md) | How a release is built and published |
| [deploy/docker](deploy/docker/README.md) | Docker Compose example |

## Building

Requirements: Go (the minimum version is the `go` line in [go.mod](go.mod)),
`make` and `git`.

```sh
git clone https://github.com/kyro-pxe/kyro.git
cd kyro
make build
./bin/kyro version
```

Other targets:

```sh
make test            # go test ./...
make lint            # gofmt, go vet, shellcheck
make fuzz            # every fuzz test, one minute each (FUZZTIME=10m for longer)
make release VERSION=v0.1.0-alpha   # release files in dist/, publishes nothing
make test-installer  # install.sh in containers; see scripts/test-installer
make e2e             # installs Windows in a virtual machine; see scripts/e2e
make clean
```

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md)
first. Everyone taking part in the project is expected to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Please do not report security issues in public issues. See
[SECURITY.md](SECURITY.md) for how to report them privately.

## License

Copyright (C) 2026 Lukas Koch and Kyro contributors.

Kyro is free software, licensed under the GNU General Public License, version 3
or (at your option) any later version. See [LICENSE](LICENSE) for the full text
and [NOTICE](NOTICE) for third-party information.

Windows is a trademark of the Microsoft group of companies. Kyro is not
affiliated with or endorsed by Microsoft and does not distribute any Microsoft
software; you need your own Windows ISOs and licenses.
