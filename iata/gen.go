// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package iata

// Regenerate iata.go from data/iata.csv using the generic csv-enum-gen tool.
//
// After editing data/iata.csv, run `go generate ./...` from this package
// (or from the repository root) to rebuild iata.go.

//go:generate go run github.com/unikraft-cloud/x/tools/csv-enum-gen data/iata.csv.json --csv data/iata.csv --out iata.go
