# SPDX-License-Identifier: BSD-3-Clause
# Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
# Licensed under the BSD-3-Clause License (the "License").
# You may not use this file except in compliance with the License.

# The environment a session starts from, run once when the shell connects.
#
# Prints env's NAME=value lines, then a line each for HOME, PATH, UID, EUID and
# GID so those exist even where the instance does not export them: HOME falls
# back to /, the ids to 0 when there is no id command. A later line wins, so
# the additions override whatever env printed for the same name.
env; printf 'HOME=%s\nPATH=%s\nUID=%s\nEUID=%s\nGID=%s\n' \
"${HOME:-/}" "$PATH" "$(id -ru 2>/dev/null || echo 0)" \
"$(id -u 2>/dev/null || echo 0)" "$(id -g 2>/dev/null || echo 0)"
