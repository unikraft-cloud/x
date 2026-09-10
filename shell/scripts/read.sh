# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# The contents of a file, for a redirection that reads it.
#
#   $1  the path
#
# The bytes go to stdout as they are; the exit status and stderr are cat's own,
# which the Go side turns into the open error.
cat -- "$1"
