# Install source spike

Test harness for the validation spike of
[ADR 0001](../../docs/adr/0001-windows-install-source.md). It boots a virtual
machine over the network into Windows PE and lets Windows Setup install from
an SMB share. The results are written up in
[docs/spikes/0001-install-source.md](../../docs/spikes/0001-install-source.md).

This is throwaway test tooling, not part of Kyro. It uses dnsmasq, Python's
HTTP server and Samba in place of the servers Kyro will have.

## What it sets up

Everything runs inside a network namespace called `kyrospike`. The namespace
contains one bridge with no uplink, so nothing in it can reach another network
and nothing outside can reach it. Samba never listens on a host address.

| Address      | Service                                                        |
| ------------ | -------------------------------------------------------------- |
| 192.168.77.1 | DHCP and TFTP (dnsmasq), HTTP, Samba with signing **required** and a named account only |
| 192.168.77.2 | Samba with default signing, guest access, also on port 4445    |

The virtual machine is QEMU with KVM, OVMF, an emulated TPM 2.0 (swtpm), an
Intel e1000e network card and an empty 64 GB disk. The firmware loads upstream
iPXE over TFTP; iPXE loads wimboot and `boot.wim` over HTTP.

## Requirements

Linux, root, and: `qemu-system-x86`, `ovmf`, `swtpm`, `dnsmasq`, `samba`,
`socat`, `netpbm`, `curl`, `python3`.

On Debian the `samba` package starts `smbd` on all interfaces when it is
installed. Stop it if you do not want that:
`systemctl disable --now smbd nmbd`.

You also need a Windows 11 ISO. The spike used the Windows 11 Enterprise
evaluation ISO from the
[Microsoft Evaluation Center](https://www.microsoft.com/en-us/evalcenter/download-windows-11-enterprise).
Do not commit it.

## Usage

```sh
export KYRO_SPIKE_ISO=/path/to/windows11.iso   # default: /var/lib/kyro-spike/iso/win11.iso
./spike.sh fetch            # download wimboot and iPXE, verify checksums
./spike.sh up               # namespace, bridge, DHCP, HTTP, Samba
./spike.sh case probe       # choose which start script wimboot injects
./spike.sh vm-start tpm fresh
./spike.sh shot /tmp/screen.png
./spike.sh click 901 636    # mouse click at screen coordinates
./spike.sh type 'dir X:\'   # keyboard input, US layout
./spike.sh vm-stop
./spike.sh down
```

`vm-start` takes `tpm` or `notpm`, and `fresh` to start with an empty disk.
Working files go to `/var/lib/kyro-spike` (`KYRO_SPIKE_DIR`). Files that the
guest writes to the `log` share end up in the `log` directory there.

## Test cases

Each file in `cases/` is a start script. wimboot injects it into
`X:\Windows\System32` together with `common/winpeshl.ini`, which makes Windows
PE run it instead of Setup. Cases that mention `autounattend.xml` also get
`common/autounattend.xml` injected.

| Case                  | What it does                                                       |
| --------------------- | ------------------------------------------------------------------ |
| `probe`               | Records what Windows PE offers and tests SMB access, then stops at a prompt |
| `setup-ram-sources`   | `X:\sources\setup.exe /installfrom:` the share                      |
| `setup-ram-root`      | `X:\setup.exe /installfrom:` the share                              |
| `setup-ram-eicfg`     | As `setup-ram-sources`, after copying `ei.cfg` from the share       |
| `setup-share-root`    | `setup.exe` from the root of the share                              |
| `setup-share-sources` | `sources\setup.exe` from the share                                  |
| `unattend-ram`        | Unattended install, Setup from RAM                                  |
| `unattend-share`      | Unattended install, Setup from the share                            |
| `labconfig-off`       | No TPM, no bypass                                                   |
| `labconfig-on`        | No TPM, LabConfig values set by the start script                    |

`common/autounattend.xml` wipes disk 0 without asking. It exists for this
harness only.
