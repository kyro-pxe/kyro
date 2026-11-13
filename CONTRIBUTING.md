# Contributing to Kyro

Thanks for your interest in Kyro. The project is in early development, so
design feedback and discussion are as useful as code right now.

By taking part you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- For anything larger than a small fix, please open an issue first so the
  approach can be discussed before you invest time.
- Read [docs/architecture.md](docs/architecture.md) for the design plan.
- Report security issues privately, as described in [SECURITY.md](SECURITY.md).

## Building and testing

You need Go (the minimum version is the `go` line in [go.mod](go.mod)), `make`
and `git`.

```sh
make build   # builds bin/kyro
make test    # runs go test ./...
make lint    # checks gofmt and runs go vet
make fuzz    # runs every fuzz test, one minute each by default
make clean   # removes build output
```

All three of `make build`, `make test` and `make lint` must pass before a pull
request can be merged. CI runs the same checks.

## Code style

- Format code with `gofmt`.
- Keep `go vet` clean.
- Add tests for new behaviour where it is practical.
- Code that parses input from the network gets a fuzz test. If you change
  such code, run `make fuzz` with a longer `FUZZTIME` before you open a pull
  request. `go test` only replays the inputs that are already recorded.
- Start every Go source file with the GPL header used in the existing files.
- Do not commit ISOs, WIM files or third-party binaries such as iPXE or
  wimboot.

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional scope>): <short description>
```

Common types are `feat`, `fix`, `docs`, `test`, `refactor`, `build`, `ci` and
`chore`. Examples:

```
feat(tftp): add read-only file server
fix(dhcp): ignore requests without PXE vendor class
docs: clarify proxyDHCP mode
```

## Pull requests

1. Fork the repository and create a branch from `main`.
2. Make your change in small, focused commits.
3. Run `make build`, `make test` and `make lint`.
4. Open a pull request against `main` and fill in the template.
5. A maintainer reviews the change. Please respond to review comments by
   pushing additional commits; the history is cleaned up when merging if
   needed.

## License and sign-off (DCO)

Kyro is licensed under the GNU General Public License, version 3 or later (see
[LICENSE](LICENSE)). Contributions are accepted under the same license.

Every commit must be signed off to certify that you have the right to submit
it under that license, as described by the
[Developer Certificate of Origin](https://developercertificate.org/). Add the
sign-off with:

```sh
git commit -s
```

This appends a line like the following to the commit message, using the name
and email from your Git configuration:

```
Signed-off-by: Jane Doe <jane@example.com>
```

Pull requests containing commits without a sign-off cannot be merged.
