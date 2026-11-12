# Security model

> **This document describes the planned design.** Kyro does not serve anything
> yet. It records what Kyro will and will not protect against, so that
> deployments can be planned around it.

To report a vulnerability, see [SECURITY.md](../SECURITY.md).

## The boot network is the trust boundary

PXE is unauthenticated by design, and Kyro cannot change that.

- A booting client broadcasts a DHCP request and acts on the answers it gets.
  Any machine in the same broadcast domain can answer as a DHCP or proxyDHCP
  server and point the client at a different boot file. Kyro cannot detect or
  prevent a rogue responder.
- Kyro in turn serves every client that asks. There is no client
  authentication.
- Boot files travel over TFTP and HTTP without encryption or integrity
  protection.
- iPXE can verify HTTPS servers, but only against trust anchors that are
  compiled into the iPXE binary. Kyro uses upstream's iPXE binaries unmodified
  and does not rebuild them, so HTTPS boot with an embedded trust anchor is
  out of scope for now.
- UEFI Secure Boot restricts which boot loaders the firmware starts. It does
  not authenticate the server they were loaded from, and it does not cover
  scripts, answer files or the install image.

Consequence: everything on the boot network has to be trusted. Run Kyro on an
isolated boot VLAN that contains only Kyro and the machines being installed.
Do not route Kyro's ports to other networks. Where the switches support it,
restrict which ports may send DHCP server replies.

## Answer files can contain secrets

An `autounattend.xml` can hold local account passwords, product keys, domain
join credentials and Wi-Fi keys. Passwords in answer files are encoded at
best, never encrypted.

Kyro will deliver the answer file of an image to any client that boots that
image, over unauthenticated HTTP. Treat everything in an answer file as
readable by everyone on the boot network.

Recommendations:

- Do not put long-lived or shared secrets in answer files.
- Enroll the machine after installation instead: device management
  enrollment, a domain join performed with a one-time or tightly scoped
  credential, or a configuration agent that fetches secrets over an
  authenticated channel.
- If a password in the answer file cannot be avoided, make it unique and
  temporary, and force a change at first logon.

## The install share

Windows Setup reads the install image from a read-only SMB share (see
[ADR 0001](adr/0001-windows-install-source.md)).

- **The share credentials are not a secret.** They are written into a start
  script that is served over HTTP. They exist because Windows clients may
  require SMB signing, and signing needs an authenticated session. Anyone on
  the boot network can read the contents of the ISOs that Kyro serves.
- **Port 445.** SMB clients connect to TCP port 445. Binding it needs
  `CAP_NET_BIND_SERVICE` or root. Kyro's embedded server cannot share an IP
  address with Samba or with Windows file sharing. On such hosts, give Kyro
  its own address on the boot interface, use an external SMB server, or
  switch the install share off.
- **Exposure.** The share is meant to be bound to the boot interface only.
  Never expose port 445 to the internet or to networks outside the boot
  VLAN.
- **New parsing code.** An embedded SMB server is security-sensitive code on
  a well-known port. It is read-only, will be fuzz-tested, and can be
  switched off in favour of an external server.

## Hardware requirement bypass

Kyro will offer an opt-in, per-image switch that lets Windows 11 Setup skip
hardware requirement checks (see ADR 0001). It is off by default. Microsoft
does not support Windows 11 on hardware below its requirements and states
that such devices are not entitled to updates.
