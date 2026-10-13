module unikraft.com/x/timeouts

go 1.27.0

require (
	github.com/stretchr/testify v1.12.1
	unikraft.com/cloud/sdk v0.3.1-0.20260928125536-cabf4afa223a
	unikraft.com/x/log v0.0.0-20260904140856-1944f6df62e1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.23 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/log v0.22.0 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

replace unikraft.com/x/log => ../log
