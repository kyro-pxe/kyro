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
> environment only: virtual machines (QEMU, UEFI, Secure Boot off) with the
> Windows 11 Enterprise evaluation ISO. It has not been tried on physical
> hardware. Kyro wipes the disks of the machines you arm, and anyone on the
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
- A machine that is not armed never gets an installer.
- Kyro does not generate answer files. You bring your own, and it fills in
  per-machine values such as `{{mac}}` or `{{serial}}`.

Kyro contains no Microsoft software. You need your own Windows ISO and
licenses. iPXE and wimboot are separate programs (GPLv2) that Kyro downloads
unmodified from their projects at setup time; see [NOTICE](NOTICE).

## Quick start

You need a Linux server (x86-64, systemd) on the same network as the
machines, a DHCP server on that network, and a Windows 11 ISO. The client
needs UEFI with **Secure Boot switched off** and network boot first in its
boot order.

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

Kyro shows the profile's warnings on import and again when you arm.

**5. Arm a client.** A profile that wipes a disk needs `--confirm-wipe`.

```sh
sudo kyro arm 52:54:00:12:34:56 --image win11 --profile kyro-example --confirm-wipe
```

**6. Boot the client from the network and watch.**

```sh
sudo kyro clients          # unknown, armed, installing, provisioned, failed
journalctl -u kyro -f
```

The state goes from `armed` to `installing` when the client fetches the
installer, and to `provisioned` when the installed system calls back. In
Kyro's own test run that took 25 minutes on a virtual machine. A client
that does not call back within `install_timeout` (3 hours) becomes `failed`.

For `provisioned` to appear, the answer file has to make the installed
system call back. `kyro profile show kyro-example --xml` shows how the
example does it with one `FirstLogonCommands` entry and the token
`{{callback_url}}`.

A read-only status page is at `http://127.0.0.1:8080` on the server.

To take an arming back: `kyro disarm <client>`. To install the same machine
again: arm it again.

## Known limitations

- **Tested in virtual machines only.** One ISO (Windows 11 Enterprise
  evaluation), QEMU with OVMF, one full run. No physical hardware, no other
  editions or languages.
- **Secure Boot is not supported.** Switch it off on the client for the
  installation.
- **UEFI x86-64 clients only.** BIOS clients are untested. No ARM.
- **Linux server only**, x86-64.
- **An existing DHCP server is required.** `dhcp.mode: full` is not
  implemented.
- **Network drivers.** Windows PE must have a driver for the client's
  network card. Kyro cannot add drivers yet.
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
- **The profile bundle format is provisional** (`"bundle_version": 0`) and
  will change.
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
