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

| Address              | Service                                                |
| -------------------- | ------------------------------------------------------ |
| 192.168.77.1         | DHCP and TFTP (dnsmasq), HTTP, Samba with signing **required** and a named account only |
| 192.168.77.2         | Samba with default signing, guest access, also on port 4445. Holds the writable `log` share. |
| 192.168.77.11 to .14 | One embedded server candidate each, started with `spike.sh cand` |
| 192.168.77.80 to .83 | The virtual machines                                   |

A virtual machine is QEMU with KVM, OVMF, an emulated TPM 2.0 (swtpm), an
Intel e1000e network card and an empty 64 GB disk. The firmware loads upstream
iPXE over TFTP; iPXE loads wimboot and `boot.wim` over HTTP. Up to four
machines can run side by side; `KYRO_SPIKE_VM` (0 to 3) selects which one a
command refers to.

## Requirements

Linux, root, and: `qemu-system-x86`, `ovmf`, `swtpm`, `dnsmasq`, `samba`,
`socat`, `netpbm`, `curl`, `python3`. Go is needed for the embedded server
candidates only.

An installed Windows takes about 11 GB of disk space per virtual machine.

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
guest writes to the `log` share end up in `log/vm<number>` there. An
unattended install is finished when `installed.txt` appears in that
directory.

To test an embedded server candidate instead of Samba, start it and point a
case at its address:

```sh
./spike.sh cand jfjallid
KYRO_SPIKE_VM=1 KYRO_SPIKE_SERVER=192.168.77.13 ./spike.sh case unattend-ram
KYRO_SPIKE_VM=1 ./spike.sh vm-start tpm fresh
```

The candidates are `gosmbserver`, `facetfs`, `jfjallid` and `absfs`. Their
test programs are in `smbcand`, which is a Go module of its own so that the
libraries do not become dependencies of Kyro.

The four libraries are MIT-licensed. The modules they pull into the test
programs are under MIT, BSD-style or Apache-2.0 licences. None of them is
linked into Kyro, so they are not listed in [NOTICE](../../NOTICE).

## Test cases

Each file in `cases/` is injected by wimboot into `X:\Windows\System32`
together with `common/winpeshl.ini`, which makes Windows PE run a start
script instead of Setup. A case that sets `SETUP` is run by
`common/start.cmd`; the others are complete scripts. Cases that mention
`autounattend.xml` also get `common/autounattend.xml` injected.

| Case                  | What it does                                                  |
| --------------------- | ------------------------------------------------------------- |
| `probe`               | Records what Windows PE offers and tests SMB access           |
| `smb-basic`           | Maps one server, lists it and copies `boot.wim` back for comparison |
| `setup-ram-sources`   | `X:\sources\setup.exe /installfrom:` the share                 |
| `setup-ram-root`      | `X:\setup.exe /installfrom:` the share                         |
| `setup-ram-eicfg`     | As `setup-ram-sources`, after copying `EI.CFG` from the share  |
| `setup-share-root`    | `setup.exe` from the root of the share                         |
| `setup-share-sources` | `sources\setup.exe` from the share                             |
| `unattend-ram`        | Unattended install, Setup from RAM                             |
| `unattend-share`      | Unattended install, Setup from the share                       |
| `labconfig-ram`       | LabConfig values set by the start script, Setup from RAM       |
| `labconfig-share`     | LabConfig values set by the start script, Setup from the share |

Run the `labconfig` cases with `vm-start notpm`. Without the bypass, use
`setup-share-root` or `setup-ram-eicfg` on a machine without a TPM.

`common/autounattend.xml` wipes disk 0 without asking. It exists for this
harness only.
