# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# What is at a path, for the interpreter's stat and lstat.
#
#   $1  the path
#   $2  non-empty to report a symlink as itself rather than what it points to
#
# Prints one line, "<kind> <size> <mtime> <special>", and exits 1 when nothing
# is there:
#
#   kind     L symlink, d directory, f regular file, p pipe, S socket,
#            b block device, c character device, o anything else
#   size     the byte count of a regular file, 0 otherwise
#   mtime    seconds since the epoch, 0 where stat cannot say
#   special  any of u (setuid), g (setgid), k (sticky), in that order
#
# Ownership is not asked for; the Go side reports every file as unowned.
p=$1
if [ -n "$2" ] && [ -L "$p" ]; then k=L; n=0
elif [ -d "$p" ]; then k=d; n=0
elif [ -f "$p" ]; then k=f; n=$(wc -c < "$p")
elif [ -p "$p" ]; then k=p; n=0
elif [ -S "$p" ]; then k=S; n=0
elif [ -b "$p" ]; then k=b; n=0
elif [ -c "$p" ]; then k=c; n=0
elif [ -e "$p" ]; then k=o; n=0
else exit 1
fi
l=-L
[ -n "$2" ] && l=
t=$(stat $l -c %Y -- "$p" 2>/dev/null || stat $l -f %m "$p" 2>/dev/null || echo 0)
m=
[ -u "$p" ] && m=${m}u
[ -g "$p" ] && m=${m}g
[ -k "$p" ] && m=${m}k
echo "$k $n $t $m"
