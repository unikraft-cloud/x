# `version`

A utility module for saving and referencing build information.

```go
import (
  "fmt"

  "unikraft.com/x/version"
)

func main() {
  fmt.Printf("App version: %s\n", version.String())
}
```

## Exposing it on the command line

Two kong types are provided for surfacing version information in a CLI.
Which one to use depends on the shape of the CLI:

- `version.VersionFlag`, for a top-level `--version` flag. Use this for
  single-purpose tools without subcommands:

  ```go
  type CLI struct {
    Version version.VersionFlag `name:"version" help:"Print version information and quit."`
    // ... other flags/args
  }
  ```

- `version.VersionCmd`, for a `version` subcommand. Use this for CLIs
  that are already structured around subcommands:

  ```go
  type CLI struct {
    Version version.VersionCmd `cmd:"" help:"Print version information."`
    // ... other commands
  }
  ```

Both can be embedded at once if a CLI wants to support `--version` as well
as `<tool> version`.

## Build-time injection

When building with the shared `go:build` task from this repository's
`Taskfile.go.yml`, version metadata is injected automatically via
`-ldflags -X`; no explicit configuration is required beyond the standard
`package` var:

```yaml
build:
  cmds:
  - task: go:build
    vars:
      package: ./cmd/my-tool
```

`name` defaults to the binary name, and `docs`/`issues` default to empty,
but all three can be overridden:

```yaml
build:
  cmds:
  - task: go:build
    vars:
      package: ./cmd/my-tool
      docs: https://unikraft.com/docs/my-tool
      issues: https://linear.app/unikraft/team/TOOL
```

To inject manually, e.g. outside of Task:

```shell
go build -ldflags='
    -X "unikraft.com/x/version.Name=my-cli-tool"
    -X "unikraft.com/x/version.Docs=https://unikraft.com/docs"
    -X "unikraft.com/x/version.Issues=https://github.com/unikraft-cloud/x/issues"
    -X "unikraft.com/x/version.Version=v0.1.0"
    -X "unikraft.com/x/version.Commit=253cd1a"
    -X "unikraft.com/x/version.BuildTime=Tue Sep 30 17:59:50 CEST 2025"
    '
```