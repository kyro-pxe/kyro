# ADR 0005: UEFI Secure Boot

- **Status:** Accepted on 2026-11-14. First as an option that was off by
  default; switched on by default the same day by the maintainer's decision.
- **Date:** 2026-11-14
- **Deciders:** Kyro maintainers

## Context

Most machines that Windows 11 is installed on ship with Secure Boot switched
on. Until now Kyro handed every UEFI client the unsigned `ipxe.efi`, which
such firmware refuses, so Secure Boot had to be switched off for the
installation and on again afterwards.

Kyro uses iPXE and wimboot as unmodified binaries from their projects'
releases and does not rebuild or sign anything
([ADR 0001](0001-windows-install-source.md)). Signing a boot loader of our
own is out of the question: it needs a key that firmware trusts.

The iPXE release that Kyro already pins (v2.0.0, `ipxeboot.tar.gz`) contains
a Secure Boot chain in its directory `x86_64-sb`:

| File | Signed by | Trusted by |
| ---- | --------- | ---------- |
| `shimx64.efi` | Microsoft ("Microsoft Corporation UEFI CA 2011") | Firmware that trusts Microsoft's key for other vendors' boot loaders |
| `ipxe.efi` | The iPXE project ("iPXE Secure Boot Automatic Code Signing G1A") | That shim |

The pinned wimboot (v2.9.0) is signed by Microsoft as well, under both the
2011 and the 2023 UEFI CA. The Windows boot manager inside `boot.wim` is
Microsoft's own.

The signers were read out of the files' signatures. Kyro does not verify
signatures; it verifies that the files are the ones it pins, by SHA-256.

## Decision

A setting, `secure_boot`, **on by default**.

- **On:** UEFI clients are told to load `ipxe-shim.efi`. The shim asks the
  same TFTP server for `ipxe.efi`, and Kyro answers that request with the
  signed iPXE. From there on nothing differs: the signed iPXE asks Kyro what
  to do, loads wimboot, and wimboot starts Windows PE. A client with Secure
  Boot switched off starts the same files.
- **Off** (`secure_boot: false`): UEFI clients are told to load `ipxe.efi`,
  the unsigned iPXE. Firmware with Secure Boot refuses it. `kyro doctor`
  warns about this setting.

A shim derives the name of the program it starts from its own name, which is
why the signed iPXE has to be served as `ipxe.efi`. In the boot directory it
is stored as `ipxe-signed.efi`, next to the unsigned `ipxe.efi`, and the
setting decides which of the two a request for `ipxe.efi` gets.

`kyro bootfiles fetch` now always fetches the two additional files. They
come out of the archive that was downloaded anyway and are checked against
compiled-in checksums like the others. Nothing is committed and nothing is
rebuilt.

### Why it is the default

Machines that run Windows 11 ship with Secure Boot switched on, and
switching it off for an installation is a visit to the firmware setup of
every machine, which on managed machines is locked or raises alarms. A
default that only works with Secure Boot off would fail on nearly every
machine Kyro is meant for. The signed chain serves both kinds of client.

What the default costs, and what to know when a machine does not boot:

- It was tested in virtual machines only (OVMF with Microsoft's keys
  enrolled, and OVMF with Secure Boot off). Firmware of real machines
  differs.
- Some machines, "Secured-core" ones in particular, do not trust Microsoft's
  key for other vendors' boot loaders unless that is switched on in the
  firmware setup ("Allow Microsoft 3rd Party UEFI CA"). The shim does not
  start there. That setting, not Secure Boot itself, is what has to change
  on such a machine.
- The signed iPXE is a different, smaller build than the unsigned one. What
  it lacks was not examined beyond the fact that Kyro's boot works with it.
  If a network card works with the unsigned iPXE and not with the signed
  one, `secure_boot: false` is the way back, for machines with Secure Boot
  off.
- The shim is signed under Microsoft's 2011 CA only. Firmware that trusts
  only the 2023 CA will refuse it. That is upstream's to solve with a new
  release.

### What Secure Boot does not give

It makes the firmware refuse boot loaders that are not signed. It does not
authenticate Kyro, does not protect the iPXE script, the start script, the
answer file or the install image, and does not stop another machine on the
boot network from answering in Kyro's place with the very same signed
binaries. [The security model](../security-model.md) is unchanged.

## Consequences

- Updating the pinned iPXE release now also updates the shim and the signed
  iPXE; all checksums change together.
- If upstream revokes a shim or a signed iPXE (through the firmware's
  revocation list), the pinned files stop booting on updated firmware until
  Kyro pins a newer release.
- BIOS clients are not affected.
- The end-to-end test runs with Secure Boot enforced by default. It first
  shows that the firmware refuses the unsigned iPXE, so that passing the
  rest means something. `KYRO_E2E_SECURE_BOOT=0` runs it with firmware that
  has Secure Boot off, against the same signed files.
