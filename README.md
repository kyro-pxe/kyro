<!-- Logo placeholder: there is no logo yet. Replace this comment with the image once one exists. -->

<h1 align="center">Kyro</h1>

<p align="center">
  An open-source PXE server for booting custom Windows 11 ISOs over the network.
</p>

<p align="center">
  <a href="https://github.com/kyro-pxe/kyro/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/kyro-pxe/kyro/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="LICENSE"><img alt="License: GPLv3" src="https://img.shields.io/badge/license-GPLv3-blue.svg"></a>
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/kyro-pxe/kyro"></a>
</p>

> [!WARNING]
> **Status: early development.** Kyro cannot boot anything yet. This repository
> currently contains the project skeleton only: a CLI with `kyro version` and a
> `kyro serve` stub that prints "not implemented yet". Everything described
> under [Planned features](#planned-features) and
> [Architecture](#architecture-planned) is a plan, not a description of
> existing functionality.

## Why Kyro?

Installing Windows over the network usually means either assembling DHCP, TFTP,
HTTP, iPXE and wimboot by hand, or using a tool such as iVentoy that makes this
convenient but is not fully open source.

Kyro aims to be the convenient option with fully open source code: one static
binary that you point at your Windows 11 ISOs, licensed under the GPLv3 so that
anyone can read, audit and change every part of it.

## Planned features

None of the following is implemented yet.

- Boot custom Windows 11 ISOs over the network without unpacking them by hand
- Built-in DHCP and proxyDHCP, so Kyro can run alongside an existing DHCP server
- Built-in TFTP and HTTP servers for the boot files
- iPXE boot menu generated from the available images
- Optional `autounattend.xml` per image for unattended installations
- Web UI for managing images
- Single static binary, with Docker and systemd deployment files

## Architecture (planned)

```
PXE client
   |
   |  1. DHCP / proxyDHCP   boot server and boot file
   |  2. TFTP               iPXE bootloader
   |  3. HTTP               iPXE script, boot menu
   |  4. HTTP               wimboot and boot files from the ISO
   v
Windows PE  ->  Windows Setup, optionally with autounattend.xml
```

iPXE and wimboot are separate third-party projects. Kyro is intended to use
them as unmodified binaries; they are not part of this repository (see
[NOTICE](NOTICE)).

See [docs/architecture.md](docs/architecture.md) for the full design plan.

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
make test    # go test ./...
make lint    # gofmt check and go vet
make clean   # remove build output
```

## Contributing

Contributions are welcome, and this early stage is a good time to discuss the
design. Please read [CONTRIBUTING.md](CONTRIBUTING.md) first. Everyone taking
part in the project is expected to follow the
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
