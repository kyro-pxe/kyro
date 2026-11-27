# Releasing

The workflow [`.github/workflows/release.yml`](../.github/workflows/release.yml)
runs **only when a tag that starts with `v` is pushed**. Pushing commits to
`main` runs CI and CodeQL, never the release workflow, so no draft release
appears until a tag exists on GitHub.

## Dry run, without a tag

On GitHub: Actions > Release > Run workflow, enter a version. The workflow
tests, builds and checks the release files and keeps them as the workflow
artifact `release-dry-run`. It creates no tag and no release.

Locally, the same build:

```sh
make release VERSION=v0.1.0-alpha   # files in dist/
```

## A release

1. CI and CodeQL are green on the commit to release.
2. `docs/ROADMAP.md`, the known limitations in `README.md` and the plan
   of the version (`docs/v0.2.0.md` and so on) describe that commit.
   `docs/releases/<tag>.md` holds the release notes; the workflow uses it
   as the text of the release and refuses to create one without it.
3. Tag and push the tag:

   ```sh
   git tag -s v0.1.0-alpha -m "Kyro v0.1.0-alpha"   # or -a without a GPG key
   git push origin v0.1.0-alpha
   ```

4. The workflow builds `kyro_<version>_linux_amd64.tar.gz`, `install.sh`
   and `SHA256SUMS` and attaches them to a **draft** prerelease.
5. Check the draft: download the three files, run
   `sha256sum --check SHA256SUMS`, and compare with
   `make release VERSION=<tag>` on the same commit (the build is
   reproducible, so the checksums must match).
6. Publish the draft by hand. Nothing is published automatically.

If the workflow fails after the tag was pushed, delete the draft (if any),
fix the cause on `main`, and move the tag only if the release was not
published yet.
