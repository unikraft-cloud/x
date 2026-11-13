# Agent notes

## Adding a new module

Each directory at the repository root (or under `tools/`) is a standalone Go
module. When adding one:

1. Create `go.mod` with the module path `unikraft.com/x/<name>`.
2. Register the module in `go.work`, keeping the relevant `use` block
   alphabetical.
3. Copy `LICENSE.md` from the repository root into the module directory.

`LICENSE.md` must be a **real file, not a symlink**: pkg.go.dev refuses to
resolve symlinks and renders the module without a license
(https://pkg.go.dev/unikraft.com/x/iata vs
https://pkg.go.dev/unikraft.com/x/stdio).

Modules containing code vendored from elsewhere keep the upstream `LICENSE`
instead of the Unikraft `LICENSE.md`, plus a `LICENSE.fragment.txt` listing
every accepted SPDX header (see `filters/` and `joinerrgroup/`).

## Pinning external dependencies

Anything pulled from outside the `unikraft-cloud` org must be pinned, so that
a moved tag or a new push upstream can't change what we run without a PR. Keep
it somewhere Dependabot can see it, so it doesn't need bumping by hand.

- **Actions**: pin by commit, with the version in a comment, e.g.
  `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`.
  Composite actions under `.github/actions/` need the same.
- **Images**: pin by tag *and* digest, e.g. `registry:3.1.2@sha256:…`. An image
  that isn't in a `Dockerfile` `FROM` (a `docker run` in a workflow, a ref in
  Go code) goes in a Dockerfile of one `FROM` line, next to where it's used:
  - Workflows run it with `docker build --quiet - <x.Dockerfile` (see
    `.github/workflows/integration/` and `.github/actions/actionlint/`).
  - Go code `go:embed`s it and reads it back with
    `unikraft.com/x/dockerfile` (see `integration/image.go`).

  Outside `.github/`, add its directory to the `docker` entry in
  `.github/dependabot.yml`.
- **Tools and scripts**: no `@latest`, no `curl … | bash` from a branch. Prefer
  an image as above, or a Go module version that Dependabot tracks.
