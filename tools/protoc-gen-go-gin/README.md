# `protoc-gen-go-gin`

A Protobuf plugin that generates idiomatic Go HTTP REST handlers and interfaces
from your service definitions, using the Gin web framework and Google API HTTP
annotations.

## Overview

`protoc-gen-go-gin` generates Go code for HTTP REST endpoints from your
`.proto` service definitions. It leverages HTTP rules defined with
`google.api.http` annotations and produces handler interfaces and registration
functions for use with the Gin router.

The plugin now supports both regular RPC methods and streaming methods:
- Regular RPC methods are exposed as standard HTTP endpoints
- Streaming RPC methods are automatically exposed as WebSocket endpoints

## Installation

Install the plugin with:

```sh
go install unikraft.com/x/tools/protoc-gen-go-gin@latest
```

Ensure your `$GOPATH/bin` (or `$GOBIN`) is in your `$PATH`.

## Usage

Run the `protoc` compiler with the `--go-gin_out` flag:

```sh
protoc \
  --go-gin_out=. \
  --go-gin_opt=paths=source_relative \
  --go_out=. \
  --go_opt=paths=source_relative \
  --proto_path=. \
  yourservice.proto
```

- `--go-gin_out=.`: Output directory for generated REST handler code.
- `--go-gin_opt=paths=source_relative`: Use source-relative import paths.
- `yourservice.proto`: Your Protobuf file.

## Example

Given the following `user.proto`:

```proto
syntax = "proto3";

package example;

import "google/api/annotations.proto";

service UserService {
  rpc GetUser(GetUserRequest) returns (User) {
    option (google.api.http) = {
      get: "/v1/users/{id}"
    };
  }
}

message GetUserRequest {
  string id = 1;
}

message User {
  string id = 1;
  string name = 2;
}
```

Run:

```sh
protoc \
  --go-gin_out=. \
  --go-gin_opt=paths=source_relative \
  --go_out=. \
  --go_opt=paths=source_relative \
  --proto_path=. \
  user.proto
```

This will generate a file like `user_rest.pb.go` with a handler interface and registration function for Gin.

## Streaming Example

The plugin supports server-side streaming RPC methods that are automatically exposed as WebSocket endpoints:

```proto
syntax = "proto3";

package example;

import "google/api/annotations.proto";

service EventService {
  // Regular RPC method
  rpc GetEvent(GetEventRequest) returns (Event) {
    option (google.api.http) = {
      get: "/v1/events/{id}"
    };
  }

  // Streaming RPC method
  rpc StreamEvents(StreamEventsRequest) returns (stream Event) {
    option (google.api.http) = {
      get: "/v1/events/stream"
    };
  }
}

message GetEventRequest {
  string id = 1;
}

message StreamEventsRequest {
  string filter = 1;
}

message Event {
  string id = 1;
  string name = 2;
  string data = 3;
}
```

For streaming methods, the generated interface requires returning two channels:

```go
// Service interface for streaming methods
type EventService interface {
  GetEvent(ctx context.Context, id string) (*Event, int, error)
  StreamEvents(ctx context.Context, req *StreamEventsRequest) (<-chan *Event, <-chan error)
}
```

Implementation example:

```go
func (s *eventService) StreamEvents(ctx context.Context, req *StreamEventsRequest) (<-chan *Event, <-chan error) {
    dataCh := make(chan *Event)
    errCh := make(chan error, 1)
    
    go func() {
        defer close(dataCh)
        defer close(errCh)

        ticker := time.NewTicker(1 * time.Second)
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                event := &Event{
                    Id:   uuid.New().String(),
                    Name: "periodic_event",
                    Data: time.Now().String(),
                }

                select {
                case dataCh <- event:
                case <-ctx.Done():
                    errCh <- ctx.Err()
                    return
                }

            case <-ctx.Done():
                errCh <- ctx.Err()
                return
            }
        }
    }()

    return dataCh, errCh
}
```

## Example: Using Buf

You can also use [Buf](https://buf.build) to generate REST handlers. Add the following to your `buf.gen.yaml`:

```yaml
version: v2
plugins:
- local: ["go", "run", "unikraft.com/x/tools/protoc-gen-go-struct@latest"]
  out: gen/go
  opt: paths=source_relative
plugins:
- local: ["go", "run", "unikraft.com/x/tools/protoc-gen-go-gin@latest"]
  out: gen/go
  opt: paths=source_relative
```

Then run:

```sh
buf generate
```

This will generate Go files, including REST handlers, in the `gen/go` directory.

## Notes

- This plugin requires your services to use `google.api.http` annotations for HTTP mapping.
- The generated code is designed for use with the Gin web framework.
- Only non-streaming RPCs are supported.

## License

See [LICENSE.md](../../LICENSE.md) for details.
