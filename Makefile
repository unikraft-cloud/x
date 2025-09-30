# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2025, Unikraft GmbH.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# Prelude
SHELL         := bash
.DELETE_ON_ERROR:
.SHELLFLAGS   := -eu -o pipefail -c
.DEFAULT_GOAL := all
Q             ?= @

# Directories
WORKDIR       := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
DISTDIR       ?= $(WORKDIR)/dist

# Tools
GO            ?= go

# Target binaries
TOOLS         ?= protoc-gen-go-gin \
                 protoc-gen-go-struct

.PHONY: tools
tools: ## Build all tools.
tools: $(TOOLS)

ifeq ($(DEBUG),y)
$(addprefix $(.PROXY), $(TOOLS)): GO_GCFLAGS ?= -N -l
else
$(addprefix $(.PROXY), $(TOOLS)): GO_LDFLAGS ?= -s -w
endif
$(addprefix $(.PROXY), $(TOOLS)): TAGS += netgo
$(addprefix $(.PROXY), $(TOOLS)): TAGS += osusergo
$(addprefix $(.PROXY), $(TOOLS)): GO_LDFLAGS += -extldflags
$(addprefix $(.PROXY), $(TOOLS)):
	(cd $(WORKDIR)/tools/$@ && \
		$(GO) build -v \
		-tags '$(subst $(SPACE),$(COMMA),$(TAGS))' \
		-o $(DISTDIR)/$@ \
		-gcflags=all='$(GO_GCFLAGS)' \
		-ldflags='$(GO_LDFLAGS)' \
		./...)

.PHONY: help
help: ## Show this help menu and exit.
	@awk 'BEGIN { \
		FS = ":.*##"; \
		printf "Unikraft Cloud X Go Modules: Developer build targets.\n\n"; \
		printf "\033[1mUSAGE\033[0m\n"; \
		printf "  make [VAR=... [VAR=...]] \033[36mTARGET\033[0m\n\n"; \
		printf "\033[1mTARGETS\033[0m\n"; \
	} \
	/^[a-zA-Z0-9_-]+:.*?##/ { \
		printf "  \033[36m%-23s\033[0m %s\n", $$1, $$2 \
	} \
	/^##@/ { \
		printf "\n\033[1m%s\033[0m\n", substr($$0, 5) \
	} ' $(MAKEFILE_LIST)
