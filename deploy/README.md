# deploy

| File | Purpose |
| ---- | ------- |
| `install.sh` | Installer for Linux servers. Installs from a release tarball with a verified checksum. See [docs/install.md](../docs/install.md). |
| `kyro.service` | The systemd unit that the installer installs |
| `config.example.yaml` | Every setting, with comments |
| `docker/` | Dockerfile and Compose example. See [docker/README.md](docker/README.md). |

`install.sh` in this directory belongs to no release and refuses to install
without `--version` or `--tarball`. The release build writes the version
into the copy that is published.
