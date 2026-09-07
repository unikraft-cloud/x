# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# Add what arrives on stdin to the end of a file, for a >> redirection.
#
#   $1  the path
#
# Same protocol as write.sh: "ok" once the file is open for appending, "no" and
# exit status 1 when it cannot be.
{ echo ok; cat >&3; } 3>> "$1" || { echo no; exit 1; }
