# Security model

This document says what Kyro does and does not protect against, as of the
alpha. Plan your deployment around the second part.

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

Kyro delivers the answer file to the client that was armed for it, over
unauthenticated, unencrypted HTTP. Anyone who can read the traffic on the
boot network reads the answer file, and a machine that claims the identity
of an armed client receives it. Treat everything in an answer file as
readable by everyone on the boot network.

Kyro shows the warnings of a profile when it is imported and again when a
client is armed for it. It does not look for secrets in the XML itself.

Recommendations:

- Do not put long-lived or shared secrets in answer files.
- Enroll the machine after installation instead: device management
  enrollment, a domain join performed with a one-time or tightly scoped
  credential, or a configuration agent that fetches secrets over an
  authenticated channel.
- If a password in the answer file cannot be avoided, make it unique and
  temporary, and force a change at first logon.

## Arming is selection, not authentication

A client is armed by its MAC address, SMBIOS UUID or serial number. The
booting machine reports all three itself, and Kyro believes it.

- Arming means: the next machine that shows this identifier is installed.
  Any machine on the boot network can show it.
- Kyro cannot tell an armed client from a machine that imitates it. The
  imitator receives the installer and the answer file, and the real client
  is then no longer armed.
- Identifiers that many machines share ("To be filled by O.E.M.", all
  zeros and the like) are refused as keys.

`kyro arm` and `kyro doctor` say this every time.

## What Kyro does to prevent accidents

These are protections against mistakes, not against attackers.

- **No installer without arming.** A client that is not armed is sent to
  its local disk. With `unknown_clients: menu` it sees a menu first, and
  nothing in that menu changes the machine.
- **One installation per arming.** The record is changed to `installing`
  and written to disk before the installer is handed out. If writing fails,
  the client gets nothing. Every later network boot is sent to the local
  disk until an administrator arms the client again.
- **Wiping needs a confirmation.** A profile that partitions or wipes a
  disk, or a bare answer file whose effect Kyro cannot know, can only be
  armed with `--confirm-wipe`.
- **A damaged record stops the service from answering boots**, rather than
  guessing a state.

## The installation token

Each arming creates a random 128-bit token. The files of that installation
and its callback are addressed with it, and it opens anything only while
the client is `installing`.

It keeps a host on the boot network from fetching answer files by guessing
profile names. It travels over plain HTTP, so it is no protection against
someone who can read the traffic.

## The callback is not authentication

The installed system reports that it is done with one HTTP request to
`/api/v1/callback/<token>`.

- It needs no credentials, and it cannot have any: whatever the installed
  system knows came from the answer file, which the boot network can read.
- Whoever knows the token can report "done" for that client. `provisioned`
  therefore means "something that knew the token said so", not "this
  machine is installed correctly". Do not build access decisions on it.
- The endpoint is strict about what it accepts: only `POST`, a body of at
  most 1 KB that is discarded, a rate limit per address, and
  the same answer for an unknown token whatever the reason. The only thing
  it can do is move one client from `installing` to `provisioned`.

## The status page

The status page and `/api/v1/clients` are read-only and have no login. They
show identifiers, states, image and profile names. They listen on
`127.0.0.1:8080` by default and on a different listener than the boot
services. If you bind them to a network address, everyone who reaches it
sees that list. Put a reverse proxy with authentication in front if you
need remote access.

## The service's privileges

On a server installed with `install.sh`, Kyro runs as its own user with
`CAP_NET_BIND_SERVICE` as its only capability, a read-only view of the file
system and write access to the client state only.
[install.md](install.md#the-service-and-its-privileges) lists every setting
and the reason for it.

The `kyro` command is run as root and changes images, profiles and boot
files. Whoever may run it can have any machine on the boot network wiped.

## Downloads

- iPXE and wimboot are downloaded over HTTPS from their projects' release
  pages and accepted only if their SHA-256 checksums equal the ones compiled
  into Kyro.
- Kyro's own releases have SHA-256 checksums, published next to the
  tarball. They detect a damaged download. They do not protect against
  someone who controls the release page. There are no signatures yet.

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
  its own address on the boot interface. Using an external SMB server
  instead is planned and not possible yet.
- **Exposure.** The share is meant to be bound to the boot interface only.
  Never expose port 445 to the internet or to networks outside the boot
  VLAN.
- **New parsing code.** An embedded SMB server is security-sensitive code on
  a well-known port. It can only read: the file system behind a share has no
  operation that writes. It is fuzz-tested. It can be switched off
  (`smb.mode: off`), but then nothing can be installed.
- **Known weaknesses of the SMB library.** Fuzzing found requests that make
  the library's parser fail, one of them before login. The library closes
  the connection of the sender and carries on. More such requests probably
  exist. [ADR 0003](adr/0003-smb-server-library.md) lists what was found and
  what Kyro does about it.
- **Denial of service.** A host on the boot network can occupy the server's
  connections and file handles up to their limits, and then other clients
  cannot install. Kyro does not prevent that; the boot network is the trust
  boundary.

## Hardware requirement bypass

Kyro will offer an opt-in, per-image switch that lets Windows 11 Setup skip
hardware requirement checks (see ADR 0001). It is off by default. Microsoft
does not support Windows 11 on hardware below its requirements and states
that such devices are not entitled to updates.
