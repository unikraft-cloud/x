# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# Whether a path can be used a given way, for the interpreter's access checks.
#
#   $1     the path
#   $2...  the tests to pass, any of -r, -w and -x
#
# Prints exactly one word: "missing" when there is nothing at the path, "denied"
# as soon as one test fails, "ok" when every test passes. Always exits 0.
p=$1; shift
[ -e "$p" ] || { echo missing; exit 0; }
for t in "$@"; do
[ "$t" "$p" ] || { echo denied; exit 0; }
done
echo ok
