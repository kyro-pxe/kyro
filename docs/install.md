# Installing Kyro on a Linux server

Kyro is alpha software. Read [the roadmap](ROADMAP.md) for what works and
[the security model](security-model.md) before you put it on a network that
other people use.

## Requirements

- Linux on x86-64 with systemd. The installer was tested on Debian 13,
  Ubuntu 24.04 and Fedora 43.
- One network interface with a fixed IPv4 address on the network of the
  clients.
- A DHCP server on that network that hands out addresses (your router, for
  example). Kyro adds the boot information next to it (proxyDHCP) and
  changes nothing on it.
- Free ports on that address: UDP 67, 69 and 4011, TCP 80 and 445.
- Disk space for your Windows ISOs (about 7 GB each), unless you link them
  with `kyro image add --link`.
- Clients: x86-64, UEFI. Secure Boot switched off, or Kyro installed with
  `--secure-boot`. See [Limitations](#limitations).

## Install

Download the installer and the checksum file of a release, check the
installer, and run it as root:

```sh
curl -fsSLO https://github.com/kyro-pxe/kyro/releases/download/v0.1.0-alpha/install.sh
curl -fsSLO https://github.com/kyro-pxe/kyro/releases/download/v0.1.0-alpha/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
sudo sh install.sh
```

The installer downloads the release tarball that belongs to it, compares
its SHA-256 checksum with `SHA256SUMS`, and stops if they differ. It does
not install from a source branch, and a copy of `install.sh` taken from the
source tree refuses to run without `--version` or `--tarball`.

The checksums guard against a damaged or swapped download. They come from
the same release page as the tarball, so they do not protect against
someone who controls that page. The alpha has no signatures.

Run from a terminal, the installer asks for the interface, the address and
whether to start the service, each with a default. With `--yes`, or without
a terminal, it asks nothing:

```sh
sudo sh install.sh --yes --interface eth1 --address 192.168.10.2
```

Without Internet access on the server, copy the tarball, `SHA256SUMS`,
`wimboot` and `ipxeboot.tar.gz` to it:

```sh
sudo sh install.sh --tarball kyro_0.1.0-alpha_linux_amd64.tar.gz --checksums SHA256SUMS \
    --bootfiles-from /path/to/bootfiles
```

`sh install.sh --help` lists every option and its environment variable.

## What the installer does

| Step | Detail |
| ---- | ------ |
| Verifies | The tarball's SHA-256 checksum, before anything is unpacked |
| Checks the input | Interface, address and DHCP mode, before anything on the machine is changed |
| Creates a user | System user and group `kyro`, no login shell, no home directory of its own |
| Installs | `/usr/local/bin/kyro`; licenses and the example configuration in `/usr/local/share/doc/kyro` |
| Creates directories | See the table below |
| Writes the configuration | `/etc/kyro/config.yaml`, only if there is none. The password of the install share is generated. |
| Fetches iPXE and wimboot | With `kyro bootfiles fetch`, from their own release pages, checked against checksums compiled into Kyro. Skip with `--no-bootfiles`. |
| Installs the service | `/etc/systemd/system/kyro.service` |
| Runs `kyro doctor` | And shows the result |
| Starts the service | Only if `kyro doctor` found no problem, and not with `--no-start` |

It never changes the firewall. `kyro doctor` prints the commands for `ufw`,
`firewalld` or `nftables`; running them is up to you.

Running the installer again is safe. It replaces the program and the unit,
keeps the configuration, the user and all data, and restarts the service if
it was running. That is also how to upgrade.

### Files and who may do what

| Path | Owner | Mode | Purpose |
| ---- | ----- | ---- | ------- |
| `/etc/kyro` | `root:kyro` | 0750 | |
| `/etc/kyro/config.yaml` | `root:kyro` | 0640 | Configuration |
| `/var/lib/kyro` | `root:kyro` | 0750 | |
| `/var/lib/kyro/images` | `root:kyro` | 0750 | ISOs and what Kyro knows about them |
| `/var/lib/kyro/profiles` | `root:kyro` | 0750 | Answer files |
| `/var/lib/kyro/boot` | `root:kyro` | 0750 | iPXE and wimboot |
| `/var/lib/kyro/state` | `kyro:kyro` | 0750 | One record per client |

The service reads all of it and writes only `state`. Images, profiles and
boot files are changed with the `kyro` command, as root.

An ISO added with `--link` stays where it is. The user `kyro` must be able
to read it; `kyro doctor` does not check that yet.

## The service and its privileges

The service runs as the user `kyro`, not as root.

### Capabilities

| Capability | Why |
| ---------- | --- |
| `CAP_NET_BIND_SERVICE` | To listen on ports below 1024 without being root: UDP 67 (DHCP), UDP 69 (TFTP), TCP 80 (HTTP) and TCP 445 (SMB). Windows PE cannot be told another port for SMB, and firmware cannot be told another port for DHCP or TFTP. |

That is the complete list. `CapabilityBoundingSet` is set to the same single
capability, so the service cannot acquire another one.

Kyro needs none of the capabilities that network boot servers often use:

- No `CAP_NET_RAW`: DHCP replies are sent as broadcasts through an ordinary
  UDP socket.
- No `CAP_NET_ADMIN`: Kyro does not configure interfaces.
- No `CAP_SYS_ADMIN` or `CAP_DAC_OVERRIDE`: ISOs are read in place with
  Kyro's own UDF reader. Nothing is mounted.

The installer's test checks this on a running service: the effective,
permitted and bounding sets are `0x400`, which is `CAP_NET_BIND_SERVICE`
alone.

### Sandbox

| Setting | Effect |
| ------- | ------ |
| `NoNewPrivileges=yes` | No program started by the service can gain privileges |
| `ProtectSystem=strict`, `ReadWritePaths=/var/lib/kyro/state` | The whole file system is read-only for the service, except the client state |
| `ProtectHome=read-only` | Home directories cannot be written. They are not hidden, because a linked ISO may live there. |
| `PrivateTmp=yes`, `PrivateDevices=yes` | Own `/tmp`; no access to hardware devices |
| `ProtectKernelTunables`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname` | No changes to the kernel, clock or host name |
| `ProtectProc=invisible` | Other users' processes are hidden |
| `RestrictAddressFamilies=AF_INET AF_INET6 AF_NETLINK AF_UNIX` | IPv4 for the services, netlink to look up the interface, Unix sockets for logging. IPv6 is listed because the Go runtime probes for it. |
| `RestrictNamespaces`, `RestrictRealtime`, `RestrictSUIDSGID`, `LockPersonality`, `MemoryDenyWriteExecute` | Closes ways to leave the sandbox |
| `SystemCallFilter=@system-service`, `SystemCallArchitectures=native` | Only the system calls an ordinary service needs |
| `UMask=0027` | Files the service creates are not readable by other users |

If you move the data directory, change `ReadWritePaths` with
`systemctl edit kyro`.

## After installing

```sh
kyro doctor                       # anything in the way?
kyro image add /path/to/windows11.iso --name win11
kyro profile import /path/to/autounattend.xml --name office
kyro arm 52:54:00:12:34:56 --image win11 --profile office
kyro clients                      # watch the state
journalctl -u kyro -f             # watch the log
```

The [README](../README.md#quick-start) walks through this with the example
profile.

## Docker

See [deploy/docker](../deploy/docker/README.md).

## Uninstall

```sh
sudo sh install.sh --uninstall    # program, unit and documents
sudo sh install.sh --purge        # also configuration, data and the user
```

`--uninstall` keeps `/etc/kyro`, `/var/lib/kyro` and the user, so that
installing again continues where you stopped. `--purge` deletes every image,
profile and client record. It asks first; without a terminal it needs
`--yes`. ISOs that were linked are not Kyro's and stay.

## Limitations

- **Secure Boot is experimental and off by default.** Without it, UEFI
  clients get an unsigned iPXE and must have Secure Boot switched off. With
  `install.sh --secure-boot`, or `secure_boot: true` in the configuration,
  they get the shim signed by Microsoft and the iPXE signed by the iPXE
  project, both unmodified from the iPXE release. That was tested in
  virtual machines only. The client's firmware must trust Microsoft's key
  for other vendors' boot loaders ("Microsoft 3rd Party UEFI CA"), which
  some machines switch off by default.
  [ADR 0005](adr/0005-secure-boot.md) has the details.
- BIOS clients are untested. The BIOS boot file is served, nothing more is
  known.
- `dhcp.mode: full` is not implemented. Kyro needs another DHCP server on
  the network.
- Only x86-64, for server and clients.
