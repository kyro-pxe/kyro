# Kyro with Docker Compose

An example, built from this source tree. There is no published image.

> **What was tested:** the image was built and run on a private Docker
> bridge network with the options of `compose.yaml` (all capabilities
> dropped except `NET_BIND_SERVICE`, `no-new-privileges`, read-only root
> file system). All services started, HTTP and TFTP answered, and the CLI
> worked inside the container. **Not tested:** a client booting from a
> container with host networking. The installer and systemd are the tested
> way to run Kyro; see [docs/install.md](../../docs/install.md).

## Why host networking

A booting client finds Kyro with a DHCP broadcast. Broadcasts do not reach a
container on Docker's bridge network, and a published port does not help.
With `network_mode: host` the container uses the host's network directly,
so everything in [docs/install.md](../../docs/install.md) about free ports
and firewalls applies to the host.

## Privileges

| Setting | Why |
| ------- | --- |
| `cap_drop: ALL`, `cap_add: NET_BIND_SERVICE` | Kyro needs one capability: listening on ports below 1024 (UDP 67 and 69, TCP 80 and 445). |
| Runs as root inside the container | Docker gives added capabilities to root only. A non-root user would need file capabilities on the program, and `no-new-privileges` switches those off. Root without capabilities cannot override file permissions, change owners, load modules or configure the network. |
| `no-new-privileges` | Nothing in the container can gain privileges |
| `read_only: true` | The image is read-only; only the data volume is written |

The image holds Kyro and CA certificates. It has no shell.

## Setup

```sh
cd deploy/docker
cp ../config.example.yaml config.yaml
# Edit config.yaml: interface, address, and a password for smb.password.
mkdir -p data isos

docker compose build
docker compose run --rm kyro bootfiles fetch     # iPXE and wimboot, into ./data
docker compose run --rm kyro doctor
docker compose up -d
docker compose logs -f
```

`bootfiles fetch` downloads iPXE and wimboot from their own release pages
and checks them against checksums compiled into Kyro. They are separate
programs under the GNU GPL version 2 and are not part of the image.

## Using it

Put your ISOs in `./isos`. Commands run inside the container:

```sh
docker compose run --rm kyro image add /isos/windows11.iso --name win11 --link
docker compose run --rm -v "$PWD/office.xml:/in.xml:ro" kyro profile import /in.xml --name office
docker compose exec kyro kyro arm 52:54:00:12:34:56 --image win11 --profile office
docker compose exec kyro kyro clients
```

`--link` records where the ISO is and does not copy it; the path must stay
valid inside the container.

The status page listens on `127.0.0.1:8080` of the host.

`kyro doctor` cannot see the host's services or firewall from inside the
container. Run the checks of [docs/install.md](../../docs/install.md) on the
host yourself.
