# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# Replace a file with what arrives on stdin, for a > redirection.
#
#   $1  the path
#
# The redirection is opened first, on descriptor 3, so a path that cannot be
# written fails before anything is read: then "no" is printed and the exit
# status is 1. Otherwise "ok" is the acknowledgement the Go side waits for
# before it starts sending, and stdin is copied into the file until EOF.
{ echo ok; cat >&3; } 3> "$1" || { echo no; exit 1; }
