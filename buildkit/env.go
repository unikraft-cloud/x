// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package buildkit

// Environment variables that official/utils/buildkit reads.
const (
	// EnvTLSCA is the CA that client certificates must chain to.
	EnvTLSCA = "BUILDKIT_TLS_CA_PEM"
	// The platform masks env values whose key ends in CERT or KEY.
	EnvTLSServerCert = "BUILDKIT_TLS_SERVER_CERT"
	EnvTLSServerKey  = "BUILDKIT_TLS_SERVER_KEY"
	// The image ignores these, Acquire reads the client's cert and key back.
	EnvTLSClientCert = "BUILDKIT_TLS_CLIENT_CERT_PEM"
	EnvTLSClientKey  = "BUILDKIT_TLS_CLIENT_KEY_PEM"

	// Each sets the buildkitd flag of the same name, lists are comma-separated.
	EnvDebug                    = "BUILDKITD_DEBUG"
	EnvDebugAddr                = "BUILDKITD_DEBUGADDR"
	EnvLogFormat                = "BUILDKITD_LOG_FORMAT"
	EnvLogLevel                 = "BUILDKITD_LOG_LEVEL"
	EnvAllowInsecureEntitlement = "BUILDKITD_ALLOW_INSECURE_ENTITLEMENT"
	EnvProxyNetwork             = "BUILDKITD_PROXY_NETWORK"
	EnvSaveCacheDebug           = "BUILDKITD_SAVE_CACHE_DEBUG"
	EnvOCIWorkerLabels          = "BUILDKITD_OCI_WORKER_LABELS"
	EnvOCIWorkerPlatform        = "BUILDKITD_OCI_WORKER_PLATFORM"
	EnvOCIWorkerSnapshotter     = "BUILDKITD_OCI_WORKER_SNAPSHOTTER"
	EnvOCIMaxParallelism        = "BUILDKITD_OCI_MAX_PARALLELISM"
	EnvOCIWorkerGC              = "BUILDKITD_OCI_WORKER_GC"
	EnvOCIWorkerGCKeepStorage   = "BUILDKITD_OCI_WORKER_GC_KEEPSTORAGE"
)
