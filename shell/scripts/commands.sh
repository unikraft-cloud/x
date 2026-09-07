# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# Every name on the instance's PATH, for command completion.
#
# One entry per line, directory by directory, unsorted and with duplicates; the
# Go side dedupes. A directory that cannot be listed contributes nothing.
IFS=:; for d in $PATH; do ls -1 "$d" 2>/dev/null; done
