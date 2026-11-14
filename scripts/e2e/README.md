# End-to-end test

`run.sh` installs Windows 11 on a virtual machine through Kyro and checks
the result. It is the test behind acceptance tests A1 to A4 in
[docs/ALPHA.md](../../docs/ALPHA.md).

```sh
sudo KYRO_E2E_ISO=/path/to/windows11.iso make e2e
```

It is not part of `make test` and does not run in the default CI.

## What you need

- Linux on x86-64 with KVM (`/dev/kvm`), and root rights for network
  namespaces and tap devices.
- `qemu-system-x86_64`, `qemu-img`, OVMF, `swtpm`, `dnsmasq`, `socat`,
  `curl`, `ip`, `setpriv` and Go.
  On Debian: `apt install qemu-system-x86 qemu-utils ovmf swtpm dnsmasq-base socat curl iproute2`.
- A Windows 11 ISO that you supply. The test was written against the
  Windows 11 Enterprise evaluation ISO from Microsoft. The ISO is linked,
  not copied, and nothing from it is ever committed.
- About 22 GB of free disk in the work directory (the test refuses to start
  with less), 6 GB of RAM for the virtual machine, and Internet access once
  to download iPXE and wimboot, unless `KYRO_E2E_BOOTFILES` points to them.

The one recorded run on the development machine (4 virtual CPUs, KVM) took
27 minutes, 25 of them for the installation. Expect something between 20
and 60 minutes.

## What it does

Everything happens in two network namespaces without a route to the
outside. The first holds a bridge with Kyro (`192.168.78.1`) and the
virtual machines. The second holds a `dnsmasq` that hands out addresses and
knows nothing about network boot, as the DHCP server of a real network
would. Kyro runs in proxyDHCP mode next to it.

Both machines have the network card first in the boot order, permanently.
Every start of a machine is a network boot, which is the situation in which
a reinstall loop would happen.

| Step | Check |
| ---- | ----- |
| 0 | `kyro serve` runs as an unprivileged user (`nobody`) whose only capability is `CAP_NET_BIND_SERVICE`, with the file permissions that `install.sh` sets up: it can write the client state and nothing else. |
| 1 | A machine that was never armed boots from the network. Kyro sends it to its disk, hands out nothing, and the disk stays empty. |
| 2 | `kyro arm` with the example profile is refused without `--confirm-wipe` and leaves no record. With it, the client is `armed`. |
| 3 | The armed machine boots, installs Windows without input, and the installed system calls back. The state is `provisioned`. The installer was handed out exactly once, although Setup restarted the machine through the network several times. |
| 4 | The machine is reset. It asks Kyro again, is sent to its disk, the state stays `provisioned`, and Windows starts from the disk (seen as a DHCP request with the vendor class `MSFT 5.0`). |

The first failed check stops the test with `e2e: FAILED: ...` and a
non-zero exit status.

## Settings

| Variable | Default | Meaning |
| -------- | ------- | ------- |
| `KYRO_E2E_ISO` | none, required | The Windows 11 ISO |
| `KYRO_E2E_DIR` | `/var/lib/kyro-e2e` | Work directory |
| `KYRO_E2E_BOOTFILES` | empty | Directory with `wimboot` and `ipxeboot.tar.gz`; they are still checked against the pinned checksums |
| `KYRO_E2E_TIMEOUT` | `5400` | Seconds to wait for the callback |
| `KYRO_E2E_RAM` | `6G` | Memory of the virtual machine |
| `KYRO_E2E_USER` | `nobody` | The user that `kyro serve` runs as |
| `OVMF_CODE`, `OVMF_VARS` | Debian's paths | The firmware. No Secure Boot keys are enrolled, so Secure Boot is off. |

## Results

Each run writes to `<work directory>/results/<time>/`: the logs of Kyro and
of the DHCP server, the output of `kyro clients`, the output of both `kyro
arm` calls, and screenshots of the machines.

After a passed run the virtual disks are deleted. After a failed run they
stay for inspection; `run.sh down` stops everything, and deleting the work
directory removes the rest.

## Working on a failure

```sh
sudo -E ./run.sh up            # network, DHCP server and Kyro
sudo -E ./run.sh vm-start 0 fresh
sudo -E ./run.sh shot 0 /tmp/screen.ppm
sudo -E ./run.sh state 0
sudo -E ./run.sh down
```

The screen of machine 0 is also reachable as VNC on the socket
`<work directory>/vm0/vnc.sock`.

The test account that the example profile creates has a public password.
The virtual machines can reach nothing but Kyro and the DHCP server.
