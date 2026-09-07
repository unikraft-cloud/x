# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# The entries of a directory, for globbing and completion.
#
#   $1  the directory
#
# The first line is the status: "missing", "notdir", "denied" or "ok". After
# "ok" comes one record per entry, each terminated by a NUL byte rather than a
# newline: "<kind> <name>", kind being d for a directory and f for anything
# else. Names come from the shell's own globs, not from ls, so a name is
# whatever the shell matched and the NUL framing carries it back intact --
# newlines included. The three patterns together cover every entry but . and
# ..; a pattern that matched nothing stays literal, which the -e/-L test drops.
d=$1
[ -e "$d" ] || { echo missing; exit 0; }
[ -d "$d" ] || { echo notdir; exit 0; }
cd -- "$d" 2>/dev/null || { echo denied; exit 0; }
[ -r . ] || { echo denied; exit 0; }
echo ok
for e in * .[!.]* ..?*; do
[ -e "$e" ] || [ -L "$e" ] || continue
if [ -d "$e" ]; then printf 'd %s\0' "$e"; else printf 'f %s\0' "$e"; fi
done
