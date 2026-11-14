# Installer test

Tests `deploy/install.sh` in fresh containers. It is the test behind
acceptance test A5 in [docs/ALPHA.md](../../docs/ALPHA.md).

```sh
KYRO_BOOTFILES=/path/to/bootfiles make test-installer
```

`KYRO_BOOTFILES` is a directory with `wimboot` and `ipxeboot.tar.gz` in the
versions Kyro pins (`kyro bootfiles fetch -h` names them). The containers
take the files from there and download nothing from the iPXE project.

Needs Docker and Go. Takes a few minutes. Not part of `make test` or of the
default CI.

## What runs

A release is built from the working tree with a test version, then:

| Round | Where | What |
| ----- | ----- | ---- |
| Plain | `debian:13`, `ubuntu:24.04`, `fedora:43`, as they come | `inside.sh`: what must be refused (no checksum, wrong checksum, changed tarball, source copy without a version, purge without confirmation), a non-interactive installation, owners and modes, a second run, uninstall, reinstall, purge |
| With tools | The same images with `iproute2` and `util-linux` added | The same, plus detection of interface and address, and an interactive installation and purge on a pseudo terminal |
| systemd | A Debian 13 container that runs systemd | `inside-systemd.sh`: the installer starts the service; it runs as `kyro` with `CAP_NET_BIND_SERVICE` and nothing else; all ports listen; HTTP and TFTP answer; the CLI and the service share the client state; upgrade restarts; uninstall stops |

The systemd container runs with `--privileged`, because systemd has to
mount file systems and manage control groups. It has its own network
namespace and none of the host's directories except the test files,
read-only.

`run.sh plain IMAGE` and `run.sh systemd` run one round.
