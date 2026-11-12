# Roadmap

This file lists what Kyro is meant to do and how far each item actually is.
It is updated in the same commit that changes an item's state.

Status values:

- **planned**: not started.
- **in progress**: being worked on, not usable.
- **working**: implemented and works when tried by hand.
- **tested**: working, and covered by automated tests or a documented,
  reproducible test run.

Kyro cannot boot anything yet.

## Phase 0: Design

| Item | Status | Notes |
| ---- | ------ | ----- |
| Decide how Windows Setup gets its install source | in progress | [ADR 0001](adr/0001-windows-install-source.md) is proposed and awaits approval. Based on research only, not on tests. |

## Phase 1: Network boot basics

| Item | Status | Notes |
| ---- | ------ | ----- |
| proxyDHCP responder | planned | Default mode. |
| Full DHCP mode | planned | Optional, after proxyDHCP. |
| TFTP server for iPXE | planned | |
| HTTP boot server | planned | |
| BIOS x86-64 clients | planned | |
| UEFI x86-64 clients, Secure Boot off | planned | |
| UEFI x86-64 clients, Secure Boot on | planned | Depends on upstream's signed iPXE and wimboot binaries. See ADR 0001. |
| Obtaining iPXE and wimboot binaries | planned | Fetched or user-supplied, never committed. |
| YAML config file and flags | planned | |
| Privileged ports: `setcap` and Docker host networking documented | planned | |
| QEMU and OVMF test setup in `scripts/` | planned | |

## Phase 2: Images

| Item | Status | Notes |
| ---- | ------ | ----- |
| Validation spike for ADR 0001 | planned | Needs a Windows 11 ISO. |
| Scan an images directory for ISOs | planned | |
| Serve boot files from the ISO through wimboot | planned | |
| Install source for Windows Setup | planned | Mechanism per ADR 0001. |
| iPXE menu generated from available images | planned | |
| Per-image `autounattend.xml` | planned | |
| Per-image NIC driver injection | planned | |

## Phase 3: Management

| Item | Status | Notes |
| ---- | ------ | ----- |
| REST API | planned | |
| Embedded web UI: list, add and remove images | planned | |
| List of clients that booted | planned | |
