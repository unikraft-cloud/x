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
