# `version`

A utility module for saving and referencing build information.

```shell
go build -ldflags='
    -X "unikraft.com/x/version.Tool=my-cli-tool"
    -X "unikraft.com/x/version.Docs=https://unikraft.com/docs"
    -X "unikraft.com/x/version.Issues=https://github.com/unikraft-cloud/x/issues"
    -X "unikraft.com/x/version.Version=v0.1.0"
    -X "unikraft.com/x/version.Commit=253cd1a"
    -X "unikraft.com/x/version.BuildTime=Tue Sep 30 17:59:50 CEST 2025"
    '
```

```go
import (
  "fmt"

  "unikraft.com/x/version"
)

func main() {
  fmt.Printf("App version: %s\n", version.String())
}
```