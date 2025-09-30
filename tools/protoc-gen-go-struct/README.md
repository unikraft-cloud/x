# `protoc-gen-go-struct`

A plugin for the Protocol Buffers compiler (`protoc`).  It generates Go struct
definitions from your `.proto` message definitions, making it easy to work with
Protobuf messages as native Go types.

## Installation

To install the plugin, run:

```sh
go install unikraft.com/x/tools/protoc-gen-go-struct@latest
```

Make sure that your `$GOPATH/bin` (or `$GOBIN`) is in your `$PATH`.

## Usage

Run the `protoc` compiler with the `--struct_out` flag, specifying the output directory:

```sh
protoc --struct_out=. --go_opt=paths=source_relative yourfile.proto
```

- `--struct_out=.`: Tells `protoc` to use the `protoc-gen-go-struct` plugin and output generated files to the current directory.
- `yourfile.proto`: Your Protobuf file.

## Example

Given the following `example.proto`:

```proto
syntax = "proto3";

package example;

message User {
  string id = 1;
  string name = 2;
  int32 age = 3;
}
```

Run:

```sh
protoc --struct_out=. example.proto
```

This will generate a Go file (e.g., `example_struct.pb.go`) with a struct like:

```go
type User struct {
  Id   string
  Name string
  Age  int32
}
```

## Example using Buf

You can also use [Buf](https://buf.build) to generate Go structs with this plugin. Add the following to your `buf.gen.yaml`:

```yaml
version: v2
plugins:
- local: ["go", "run", "unikraft.com/x/tools/protoc-gen-go-struct@latest"]
  out: gen/go
  opt: paths=source_relative
```

Then run:

```sh
buf generate
```


This will generate Go struct files in the `gen/go` directory.

## Notes

- This plugin is intended for simple use cases where you want plain Go structs from Protobuf messages.
- It does not generate serialization or RPC code—only struct definitions.


## License

See [LICENSE.md](../../LICENSE.md) for details.
