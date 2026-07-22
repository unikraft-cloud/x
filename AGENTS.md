# AGENTS

## Purpose

This repo contains shared Go modules used across Unikraft Cloud systems. Use
this file for contributor-facing guidance.

Public mirror: [unikraft-cloud/x](https://github.com/unikraft-cloud/x). Module
prefix: `unikraft.com/x/<module>`.

## Workspace layout

Multi-module Go workspace (`go.work`, Go 1.26.3). Each directory under the root
is an independent module with its own `go.mod`. The root module
(`unikraft.com/x`) exists only to host `replace` directives for tools — it
publishes no packages.

**Common modules** (`go.work`, alphabetical — keep this order when adding):

`colors`, `filters`, `fingerprint`, `guesstermwidth`, `iata`, `image-spec`,
`joinerrgroup`, `kingkong`, `kraftfile`, `limiter`, `log`, `middleware`,
`oidext`, `ptr`, `ringbuffer`, `router`, `sanitize`, `startstopper`,
`telemetry`, `text`, `version`.

**Tools** (`package main`): `tools/csv-enum-gen`, `tools/imgtool`,
`tools/kraftfile-schema`, `tools/license-check`, `tools/openapi-gen`,
`tools/protoc-gen-go-gin`, `tools/protoc-gen-go-struct`.

`buf.yaml` covers `tools/` (proto modules for the protoc plugins + their
`testdata/`).

## Development practices

- Use [`task`](https://taskfile.dev/) for builds and workflows. The root
  `Taskfile.yml` includes a shared `Taskfile.go.yml` that defines all the
  per-module Go tasks.
- Build all tools: `task tools`. Build one tool: e.g. `task imgtool`.
- Quality gates run **per module** via the workspace matrix:
  - `task ci/check` — runs `static-check` (tidy?, fmt, lint, license) in every
    module via `task forall`.
  - `task ci/test` — runs `gotestsum` in every module.
  - `task ci/build` — builds all tools.
- Run a task in a single module: `task forone module=<path> gotask=<task>`
  (e.g. `task forone module=./log gotask=test`).
- Run a task in all modules: `task forall gotask=<task>`.
- Per-module tasks (defined in `Taskfile.go.yml`): `generate`, `buf`, `build`,
  `snapshot`, `test`, `tidy`, `tidy?` (check-only), `lint`, `fmt`, `license`,
  `static-check`, `ci/{check,test,build}`.
- Run lint/test/static-check locally before pushing; CI also runs them.

## Shared toolchain (`Taskfile.go.yml`)

This file is consumed remotely by other repos (`unikraft-cloud/agent`,
`unikraft-cloud/cli`, `unikraft-cloud/z`) via:

```yaml
includes:
  go:
    taskfile: https://github.com/unikraft-cloud/x.git//Taskfile.go.yml?ref=prod-staging
```

Changes here propagate to every consumer on next CI run. Be deliberate.

Defaults baked in:

- `go build` uses `-buildmode=pie`, debug flags when `debug=true`, version
  stamping via `-X "<module>/internal/version.{Version,Commit,BuildTime}"`.
- `gotestsum` for tests; `golangci-lint v2` for lint; `gofumpt` for formatting.
- `license` task invokes `unikraft.com/x/tools/license-check@prod-staging`
  against `*.go` (excludes `*.pb.go`, `*.gen.go`).
- The default `golangci_config` enables: `staticcheck`, `errcheck`, `govet`,
  `modernize`, `unparam`, `ineffassign`, `unused`, `misspell`, `godox`,
  `testifylint`, `nolintlint` (`require-explanation`, `require-specific`).
  Staticcheck disables `ST1000/ST1003/ST1016/ST1020/ST1021/ST1022` and
  `SA5008` (kong uses duplicate struct tags).

## Module reference

Each module's top exported API (the symbols callers should reach for):

| Module | Purpose | Key API |
|---|---|---|
| `colors` | Terminal palette on `charm.land/lipgloss` | `PrimaryFg`, `SuccessFg`, `WarningFg`, `ErrorFg`, `InfoFg` (and `*FgBg`); raw `Blue500`, `Emerald500`, … |
| `filters` | Containerd-style filter language (`==`, `!=`, `~=`, wildcards) | `Filter` iface (`Match(Adaptor)`), `FilterFunc`, `Always`, `Any`, `All`, `Adaptor` |
| `fingerprint` | Hardware/OS fingerprint for telemetry + license binding | `Fingerprint` struct (`oid:`-tagged), `New() (*Fingerprint, error)` |
| `guesstermwidth` | Terminal width detection (`COLUMNS` / `TIOCGWINSZ`, fallback 80) | `GuessTermWidth(w) int`, `IsTTY(w) bool` |
| `iata` | IATA airport code enum (generated from CSV) | `Iata`, `IataXXX` consts, `ToIata(string) Iata`, `Values() []Iata` |
| `image-spec` | Unikraft unikernel image packaging over OCI manifests | `Image`, `NewImage(opts...)` (`WithKernel/WithInitrd/WithRom/...`), `File`, `Accessor`, `LoadOCILayout/LoadTarball/LoadRegistryImage`, `SaveOCILayout/SaveTarball/SaveRegistryImage`, `ParseURI` |
| `joinerrgroup` | `golang.org/x/sync/errgroup` fork that **joins all errors** | `Group`, `WithContext`, `Go`, `TryGo`, `SetLimit`, `Wait() error` |
| `kingkong` | ANSI help printer for `alecthomas/kong` | `HelpPrinter(version)`, `AdditionalHelp`, `ExamplesProvider`, `GroupCommands`, `GroupFlags` |
| `kraftfile` | Kraftfile YAML schema (parse/validate/merge) | `Kraftfile`, `ParseFile/ParseDirectory/ParseBytes(... ParseOpt)`, `Validate([]byte)`, `JSONSchema()`, `(*Kraftfile).Merge` |
| `limiter` | Semaphore-channel concurrency limiter | `Limiter`, `NewLimiter(n)`, `Go(ctx, func) bool`, `Wait()`, `Close()` |
| `log` | `rs/zerolog` wrapper with containerd-style `log.G(ctx)` | `G = FromContextOrDefault`, `WithLogger(ctx, *Logger)`, `New(w, Type, Level)`, `JSONType`/`TextType` |
| `middleware` | Gin HTTP middleware + OpenAPI response writers | `Logger`, `CORS`, `CacheControl`, `ExtraHeaders`, `RequestID`, `Telemetry`, `NegotiateAccept`, `WriteResponse`, `EncodeJSONResponse` |
| `oidext` | Encode Go structs to ASN.1 OID-based X.509 extensions | `Encode(prefixOID, v, opts...)`, `Decode(prefixOID, exts, out, opts...)`, `All(prefixOID, structPtr)` |
| `ptr` | Generic pointer helpers | `ToPtr[T](v) *T` (preferred; `Ptr` is deprecated), `FromPtr`, `NilIfZero`, `ValueOrDefault`, `SafeDeref`, `ErrorIfNil` |
| `ringbuffer` | Fixed-capacity ring buffer (overwrites oldest) | `RingBuffer[T]`, `NewRingBuffer[T](cap)`, `Push/Pop/Peek/Last/ToSlice` |
| `router` | Gin wrapper with sane timeouts, graceful shutdown, renderers | `Router`, `New(ctx, addr, opts...)`, `RouteHandler`, `WithDebug/WithRoutes/WithGlobalMiddleware/...`, `HTMLTemplRenderer` |
| `sanitize` | Redact secrets/PII from error messages | `SanitizeErrorMessage(msg) string` |
| `startstopper` | Lifecycle contract interface | `StartStopper interface { Start(context.Context) error; Stop(context.Context) error }` |
| `telemetry` | OpenTelemetry bootstrap from env (`OTEL_*`) | `Init(ctx) (shutdown func(context.Context) error, error)` |
| `text` | ANSI-aware text utils | `DisplayWidth(s) int`, `Truncate(maxWidth, s) string` |
| `version` | Build metadata + kong `VersionFlag` | `Version`, `Commit`, `BuildTime`, `Tool`, `String()`, `UserAgent()`, `VersionFlag` |

## Tools reference

All tools are `package main`. `csv-enum-gen`, `kraftfile-schema`, and
`openapi-gen` are not in the root `Taskfile.yml` build aliases (though
`openapi-gen` ships a committed binary and is invoked remotely by other repos).

| Tool | Purpose | Invocation |
|---|---|---|
| `tools/csv-enum-gen` | Render CSV → Go "fat enum" via a text/template | `csv-enum-gen <config.json> [--csv path] -o path` |
| `tools/imgtool` | Inspect / copy / delete OCI images (honors docker creds) | `imgtool {inspect\|copy\|delete} <image> [--insecure ...]` |
| `tools/kraftfile-schema` | Emit the Kraftfile JSON Schema | `kraftfile-schema [-o path]` |
| `tools/license-check` | Verify every `*.go` has the required license fragment | `license-check <path...> [--include ...] [--exclude ...] [--no-gitignore]` |
| `tools/openapi-gen` | OpenAPI 3.0 → Go templates (`go-client`, `go-server`/Gin) | `-i input -o output -t templates [-v k=v] [--tag ...] [--namespace ...]` |
| `tools/protoc-gen-go-gin` | protoc plugin → Gin HTTP handlers from `google.api.http` | `protoc --go-gin_out=.` (plugin flags: `stream_keepalive`, `stream_data_prefix`, `base_package`) |
| `tools/protoc-gen-go-struct` | protoc plugin → plain Go structs from `.proto` messages | `protoc --struct_out=.` (plugin flags: `base_package`, `native_time`, `omit_path_params`, `help_tag`) |

`openapi-gen` and the protoc plugins are consumed remotely as
`go run unikraft.com/x/tools/<name>@<ref>` by other repos — keep their CLI
surface stable.

## Generated artifacts

- `iata/iata.go` — generated by `tools/csv-enum-gen` from `iata/data/*.csv`.
  Do not edit by hand.
- `kraftfile/schema.{json,md}` — generated by `tools/kraftfile-schema` from
  `kraftfile.JSONSchema()`. Do not edit by hand.
- `kingkong/testdata/{help.golden,help-detail.golden}` — golden help output.
  Update via the test's update mechanism, not by hand.
- `tools/protoc-gen-go-struct/testdata/**` — golden outputs per scenario.
- `tools/protoc-gen-go-struct/tags/tags.pb.go` — generated from `tags.proto`.

`license-check` excludes `*.pb.go` and `*.gen.go` by default.

## Important dependencies

- `alecthomas/kong` — CLI parsing (tools, kingkong)
- `charm.land/lipgloss`, `muesli/reflow` — terminal styling / text wrapping
- `rs/zerolog` — backing logger for `log`
- `gin-gonic/gin` — HTTP framework for `middleware` and `router`
- `google/go-containerregistry`, `opencontainers/image-spec`,
  `containerd/containerd` — backing `image-spec`
- `kin-openapi` — OpenAPI parsing for `openapi-gen`
- `bufbuild/buf` — proto tooling (`buf generate`)
- `smallstep/certificates` — referenced by `z/pki` (sibling repo)

## Advice

- If modifying existing functionality that has tests, add new tests to cover
  the changes in behavior. Tests live next to the code as `*_test.go`.
- If wanting to explore Go documentation or inspect Go code, use the `go doc`
  command - don't explore the filesystem outside of the current directory for
  this.
- For terminal/TUI color styling, use `unikraft.com/x/colors` tokens instead of
  arbitrary hard-coded color values.
- If writing multi-line strings, use backticked strings.
- Use the newest Go features you know about; do not worry about old Go
  versions (workspace Go 1.26.3+). For example:
  - Use `errors.Join` for combining errors.
  - Use the `slices`/`maps`/`cmp`/`iter` packages for common operations on
    slices/maps/comparisons/iteration.
  - Use `for i := range <n>` for iterating a fixed number of times.
  - No need to use `i := i` in loops to capture loop variables for closures;
    Go 1.21+ captures them by default.
  - `new(value)` is valid syntax as of Go 1.26 (previously `new` only accepted
    a type). Use it freely for pointers to literals — e.g. `new(1)` or
    `new(SomeStruct{...})` — instead of a helper like `ptr.To(1)`.
  - Use the `testify` package when writing tests like in the current tests.
- Adding a new common module: create `<name>/` with its own `go.mod`
  (`module unikraft.com/x/<name>`), add it to **both** `use (...)` blocks in
  `go.work` (common modules list is alphabetical — preserve ordering), add a
  row to the table above.
- Adding a new tool: create `tools/<name>/` as `package main` with its own
  `go.mod`, add to `go.work` tools block (alphabetical), add a build alias to
  the root `Taskfile.yml` and include it in the `tools` aggregate task.
- Modules here must stay focused and dependency-light — they are pulled
  transitively into every Unikraft service. Avoid adding heavy deps to small
  utility modules; consider a new module instead.
- Match `gofumpt` formatting (enforced by `task fmt`).
