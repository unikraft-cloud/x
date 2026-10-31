// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

// Package mergefs merges the tar layers of an OCI image into one read-only
// fs.FS, with the whiteout, opaque and hard link rules of docker.
//
// # Order of the merge
//
// Merge reads each layer one time, from the top layer down to the base layer.
// When it reads an entry, it already knows the upper layers, thus it knows if
// the entry stays visible. It writes the data of a visible file to the spool,
// and it does not write the data of a hidden file to the spool:
//
//	layer 2 (top)   usr/bin/git (v2)   visible: the data goes to the spool
//	layer 1         usr/bin/git (v1)   hidden by layer 2
//	layer 0 (base)  etc/passwd         visible: the data goes to the spool
//
// Entry i of layer l has the rank (l, i). The ranks put the entries in the
// order in which an unpacker applies them, base layer first. Merge uses the
// ranks to find the header that sets the metadata of a path, and the entries
// that an opaque directory or a replaced directory hides.
//
// # Hard links
//
// A hard link of layer l points at its target as layer l sees it: an earlier
// entry of layer l, or else the entry of a lower layer. Merge reads the lower
// layers later, thus such a link waits as a pending link until Merge reads its
// target. A link without a target hides the lower layers, as a whiteout does.
//
// A link can need the data of a hidden file of its own layer. For example,
// layer 1 has usr/bin/git and a hard link to it, and layer 2 replaces only
// usr/bin/git. When the link comes, the stream of layer 1 is already after
// usr/bin/git. Thus Merge writes the data of the hidden files of the current
// layer to a scratch file, and a link that needs one copies it to the spool.
// At the end of each layer, Merge empties the scratch file.
//
// # Spool
//
// The spool is a temporary file with the data of the regular files of the
// result, and FS reads the file data from it. Merge removes the spool and the
// scratch file from their directory when it makes them, or when it closes them
// on a system that cannot remove an open file. With InMemory, Merge keeps the
// data in memory and does not make these files.
package mergefs
