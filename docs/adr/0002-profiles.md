# ADR 0002: Profiles

- **Status:** Accepted on 2026-11-14. This record is the specification of
  the bundle format (see [Bundle format](#bundle-format)); the generator on
  the Kyro website is built from it and follows it.
  Automatic selection by rules is not part of the alpha; clients are armed
  one at a time ([ADR 0004](0004-client-state.md)).
- **Date:** 2026-11-12
- **Deciders:** Kyro maintainers

## Context

Installing Windows unattended needs an answer file (`autounattend.xml`). The
usual way to have many configurations is to build one ISO per configuration,
each several gigabytes large and almost identical to the others.

Kyro already controls what Windows PE sees at boot: wimboot injects files next
to an unmodified `boot.wim` ([ADR 0001](0001-windows-install-source.md)). So
the answer file does not have to be inside the ISO. One ISO can serve any
number of configurations if Kyro picks the answer file per boot.

The answer files come from a generator on the Kyro website, which lives in a
separate repository (`kyro-pxe/website`). The user downloads the result and
imports it into Kyro. Kyro stores, validates and serves these files. It does
not generate XML and does not interpret the generator's options.

## Decision

### Images and profiles are separate

- An **image** is a Windows ISO. It is large and stored once.
- A **profile** is an answer file plus a little metadata. It is small. A few
  hundred profiles cost a few megabytes.

A boot menu entry is a pair of image and profile. Every image also has an
entry without a profile, which starts Setup interactively.

A profile can declare which Windows builds it supports. Kyro reads the build
number of each image from the image itself and offers only matching pairs. A
profile without a declaration is offered for every image.

With many profiles a flat list of all pairs is unusable, so the menu is
two-level: choose the image, then one of the profiles that fit it. The menu
itself is built in Phase 2.

### Bundle format

Profiles are exchanged as a **bundle**: a JSON document that carries the
answer file and its metadata. What this record relies on:

- a `version` field, so the format can change without guesswork;
- the answer file;
- a list of warnings written by the generator, for example that the file
  contains a password or wipes a disk without asking;
- the Windows builds the profile supports;
- a size limit of 2 MiB for the whole bundle.

**This table is the specification.** The website's generator is built from
this document, not the other way round: a change to the format is made here
first, and the generator follows.

**Version 0.** The format carries version 0 while Kyro is an alpha. Until
version 1, a field may be added, renamed or removed in a new release of
Kyro; such a change is recorded in this document, and a Kyro that meets a
version it does not read refuses the bundle and says which versions it
reads. Version 1 will be the first one that later releases promise to keep
reading.

| Field | Type | Required | Meaning |
| ----- | ---- | -------- | ------- |
| `bundle_version` | number | yes | `0` |
| `name` | string | no | Profile name. `--name` overrides it. |
| `description` | string | no | Free text, at most 2,000 bytes |
| `wipes_disk` | boolean | yes | Whether the answer file partitions, formats or wipes a disk without asking |
| `warnings` | list of strings | no | At most 32, each at most 1,000 bytes |
| `windows_builds` | list of strings | no | Build numbers such as `"26100"`. Stored and shown; the alpha does not match them against images. |
| `answer_file` | string | yes | The content of `autounattend.xml` |

Fields that are not in this table are an error.

A JSON Schema is not published for version 0. It will be for the shared
format.

Kyro also accepts a **bare `.xml`** answer file. It is treated as a profile
with no warnings and no build declaration. Its name comes from `--name` or,
if that is absent, from the file name.

Whether an input is a bundle or a bare answer file is decided by its content
(first significant byte `{` or `<`), not by its file extension.

### Storage

Profiles live in a profiles directory, one subdirectory per profile:

```
profiles/
  office-laptop/
    autounattend.xml    the answer file, byte for byte as imported
    profile.json        metadata: bundle fields, import time, SHA-256
```

- The answer file is never re-serialised. Kyro cannot know what Windows Setup
  tolerates, so it must not change whitespace, namespace prefixes or
  attribute order.
- Profile names match `[a-z0-9][a-z0-9-]{0,62}`. That makes them safe as
  directory names, URL path segments and menu labels.
- Import writes to a temporary directory and renames it into place. Importing
  under a name that exists fails unless `--replace` is given.

### Validation on import

Import is the only point where a profile enters Kyro, so everything is checked
there. A file that fails any check is not stored.

1. **Size.** At most 2 MiB are read. Anything longer is rejected before
   parsing.
2. **Bundle version.** `version` is read first. An unknown version is rejected
   with a message that names the version found and the versions this Kyro
   supports, instead of a confusing schema error.
3. **Bundle schema.** Required fields, types, lengths, no unknown fields.
4. **Encoding.** The answer file must be UTF-8, with or without a byte order
   mark. Other encodings are rejected. Substitution works on bytes, so this
   keeps it sound.
5. **Well-formed XML.** The whole file is parsed strictly, to the end.
6. **No DOCTYPE, no entity declarations.** Any `<!DOCTYPE` is rejected, which
   also rules out internal subsets, `<!ENTITY>`, external entities and
   entity-expansion bombs. Only the five predefined entities and numeric
   character references remain possible. Go's XML parser does not resolve
   external entities anyway. The check protects the parsers that read the
   file after Kyro: Windows Setup, and anything else the file is shown in.
7. **No processing instructions** other than the XML declaration.
8. **Root element** `unattend` in the namespace
   `urn:schemas-microsoft-com:unattend`. This is a sanity check that the file
   is an answer file at all. Kyro does not validate settings.
9. **Tokens.** See below. A malformed use of a known token is an error.

### Token substitution

An answer file may contain tokens that Kyro replaces for each client when it
serves the file:

| Token        | Value                                   | Form after sanitising      |
| ------------ | --------------------------------------- | -------------------------- |
| `{{mac}}`    | MAC address of the booting interface    | 12 hex digits, upper case  |
| `{{serial}}` | SMBIOS serial number                    | as reported, filtered      |
| `{{uuid}}`   | SMBIOS system UUID                      | 8-4-4-4-12, lower case     |
| `{{callback_url}}` | Where the installed system reports that it is done | an `http` URL |

A suffix `:N` keeps the **last** N characters of the value, for example
`PC-{{serial:12}}`. N is 1 to 64. The first characters are the vendor part
of a MAC address and the model part of most serial numbers, the same across
a fleet; the last ones differ. `{{callback_url}}` takes no suffix.

`{{callback_url}}` is the one token whose value comes from Kyro and not from
the client. It is made of Kyro's address and the token of the installation
([ADR 0004](0004-client-state.md)) and consists of letters, digits and
`: / . _ -` only. An answer file uses it to tell Kyro that the installation
is complete, for example in a first logon command:
`curl.exe -X POST {{callback_url}}`.

The values arrive as query parameters of the request that iPXE makes. They
are untrusted. Each value passes two independent layers:

1. **Sanitising.** Every character outside `[A-Za-z0-9-]` is removed, leading
   and trailing hyphens are removed, and the result is capped at 64
   characters. A MAC address must then be 12 hex digits and a UUID must parse
   as one. The leading hyphen is removed because the value can end up as a
   command argument inside a script.
2. **Escaping** for the place the value is written to: XML escaping in text,
   and a split of `]]>` inside CDATA. With the character set above both are
   no-ops. They exist so that a later token with a wider character set, or a
   mistake in the sanitiser, does not become an injection.

If a profile uses a token and the client supplies no usable value, Kyro
refuses to serve the answer file and logs why. iPXE then fails on that menu
entry, on screen, before Windows starts. The alternative is a computer named
after an empty string and a Setup error half an hour later.

**Where tokens are replaced: in text nodes and in CDATA sections. Nowhere
else.**

- Text and CDATA are the same thing to the program that reads the file:
  character data. Which of the two a generator emits is a serialisation
  choice. Scripts embedded in answer files often sit in CDATA, and a script
  is exactly where a computer name or a serial number is useful. Excluding
  CDATA would make tokens work or silently not work depending on how the
  generator happened to quote.
- CDATA has no escaping, so there the sanitiser carries the weight. The
  allowed characters cannot form `]]>`, and they cannot form anything that
  cmd, PowerShell or a registry file treats specially.
- Attribute values are excluded. Answer files use attributes for structure
  (`pass`, `name`, `wcm:action`), not for values a client should influence.
- Comments are excluded. `--` is illegal inside a comment, so even the safe
  character set could make the file malformed there.
- Element names, attribute names and processing instructions are excluded,
  because a value there would change the structure of the document.

A token outside the allowed places is served unchanged, and import reports it
as a warning. Text of the form `{{...}}` that is not a Kyro token is also
served unchanged and reported, because scripts may use that syntax for their
own purposes. A known token with an invalid suffix, such as `{{mac:0}}`, is an
import error.

Tokens are matched on the raw bytes of the file. `&#123;&#123;mac}}` is
therefore not a token, which gives authors a way to write a literal one.

### Computer names and other per-machine values

Kyro has no setting for the computer name, for passwords or for anything else
that an answer file decides. Decided by the maintainer on 2026-11-13.

- If the answer file sets `ComputerName`, with or without a token, Windows
  uses that name. `PC-{{serial}}` is the way to give every machine a
  predictable name without one profile per machine.
- If the answer file does not set it, Windows Setup picks a name of its own.
  Kyro does not add one.
- Whatever else the answer file does is passed through unchanged. That
  includes scripts that stop and ask the operator for input, such as a
  console window that asks for a password. Kyro neither generates such
  prompts nor removes them.

### Warnings and other metadata

- The bundle's warnings are shown when a profile is imported and in
  `kyro profile show`.
- Kyro cannot verify them. A bundle without warnings is not a statement that
  the file is harmless, and a bare `.xml` has none by definition. Every import
  therefore also prints a fixed notice: answer files can wipe disks and
  contain credentials, review them before use.
- All metadata strings are untrusted. Before they are printed to a terminal,
  control characters are removed, so that a bundle cannot inject terminal
  escape sequences. The same strings must be HTML-escaped in the web UI.
- Bundles are not signed. Importing one is an administrative action on the
  Kyro host.

### Disks

An answer file can wipe a disk without asking. Kyro refuses to arm a client
for such a profile unless the administrator confirms it (`--confirm-wipe`).
Which profiles need that:

- a bundle that says `"wipes_disk": true`;
- a bundle that says `false` but whose answer file has a
  `<DiskConfiguration>` section or mentions `diskpart`, `Clear-Disk`,
  `Initialize-Disk`, `Format-Volume` or `New-Partition`. Kyro then tells the
  person importing it that the bundle contradicts itself;
- every bare answer file, because it declares nothing.

This is the one place where Kyro looks into an answer file. It is a search
for a few words, not an understanding of the file, and it errs towards
asking.

### Serving

Kyro serves the rendered answer file over unauthenticated HTTP, under a path
that contains the token of one installation, and only while that client is
`installing`. From there wimboot places it in Windows PE's RAM disk.

- The token keeps a host on the boot network from fetching profiles by
  name. It does not protect against someone who can read the traffic.
  Everything in a profile must still be treated as readable on that
  network. This is why [the security model](../security-model.md) asks for
  an isolated boot VLAN and recommends enrolling machines after
  installation instead of putting passwords into answer files.
- The values of the tokens are taken from the boot request that started the
  installation and stored with the client record. A later request cannot
  change them.
- Kyro logs which profile was served to which client, never the rendered
  content.

### Automatic selection

Not part of the alpha. What follows is the plan.

Optionally, rules in the configuration select a profile from the client's
MAC address, UUID or serial number. The first matching rule wins.

```yaml
profiles:
  rules:
    - match: { mac: "52:54:00:77:00:01" }
      image: win11-ent
      profile: lab-kiosk
    - match: { serial: "PF2*" }
      profile: office-laptop
```

A matching rule preselects the menu entry and starts a countdown. The
operator can still choose something else.

**This is selection, not authentication.** All three identifiers are sent by
the client and are trivial to forge. A rule decides what a cooperative
machine is offered by default. It does not restrict who can obtain a profile,
and it must never be used as an access control.

Two hazards follow from combining rules with unattended profiles, and the
documentation has to state both:

- A machine that boots from the network first and matches a rule reinstalls
  itself on every boot. Put the disk first in the boot order.
- A rule that matches broadly offers a disk-wiping profile to machines nobody
  meant to reinstall.

Rules are defined here and implemented together with the menu.

## Consequences

- Hundreds of configurations need one ISO per Windows release instead of one
  per configuration.
- The bundle format is a contract between two repositories. Changing it means
  a new `version` and a coordinated release.
- Kyro's validation is deliberately shallow. It guarantees that a stored
  profile is a well-formed, DOCTYPE-free answer file. It does not guarantee
  that Windows Setup accepts it.
- Substitution needs a parser that knows where text and CDATA are, not a
  plain search and replace. That is more code, and it gets fuzz tests.
- Delivery to Windows Setup follows ADR 0001: wimboot injects the file and
  the start script passes it with `/unattend:`.

## Open questions

1. **Countdown for automatic selection.** Whether a rule may boot without a
   countdown at all. Not relevant before rules exist.

Settled since this record was proposed: this record is the specification
of the bundle format, and the website follows it; `{{callback_url}}` stays;
`:N` keeps the last N characters,
and a MAC address is written as 12 hexadecimal digits in upper case.
