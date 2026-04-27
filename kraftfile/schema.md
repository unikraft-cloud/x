---
title: Kraftfile Reference (v0.7)
description: |
  This document contains information about how to write a `Kraftfile` which is
  used to configure, build, package and deploy your application as a Unikraft
  unikernel.
---

The `Kraftfile` is the static configuration file used to programmatically build, run, package and deploy a unikernel.
This document contains information about how to set that configuration including how to program Unikraft's core build system, third-party libraries, syntax options and more.

A `Kraftfile` is typically found at the top-level of a repository.

## File names

The following file names are automatically recognized where `Kraftfile` is the preferred name:

- `Kraftfile`
- `kraft.yaml`
- `kraft.yml`

## Top-level `spec` attribute

All `Kraftfile`s MUST include a top-level `spec` attribute which is used to both validate as well as correctly parse the rest of the file.
The spec version for this document is `v0.7`:

```yaml
spec: v0.7
```

The `spec` element can also be specified as `specification`, for example:

```yaml
specification: v0.7
```

Only one of `spec` or `specification` may be specified — not both.

## Top-level `name` attribute

An application `name` CAN be specified, for example:

```yaml
spec: v0.7

name: helloworld
```

When no `name` attribute is specified, the directory's base name is used.

## Top-level `cmd` attribute

A `cmd` attribute CAN be specified as an array or string which can be used for setting default arguments to be used during the instantiation of a new unikernel instance.

### Specified as an in-line array

```yaml
spec: v0.7

cmd: ["-c", "/nginx/conf/nginx.conf"]
```

### Specified as a multi-line array

```yaml
spec: v0.7

cmd:
  - -c
  - /nginx/conf/nginx.conf
```

### Specified as a string

When specified as a string, the value is shell-parsed into individual arguments:

```yaml
spec: v0.7

cmd: "-c /nginx/conf/nginx.conf"
```

### Specifying kernel parameters

The `cmd` attribute respects the Unikraft `uklibparam` convention of separating kernel arguments from application arguments via the `--` delimiter:

```yaml
spec: v0.7

cmd:
  # Kernel arguments
  - env.vars=[ "HOME=/" ]
  # Delimiter
  - --
  # Application arguments
  - -c
  - /nginx/conf/nginx.conf
```

## Top-level `env` attribute

An `env` attribute CAN be provided which injects environmental variables to the application.

Compared to when specifying them in the `cmd` attribute, using the `env` attribute is more context-aware.
Tooling recognizes where it can be best injected, for example, embedded as static variables during build-time, as OCI environmental variables when packaging, as command-line arguments, etc.

### Specified as a list

```yaml
spec: v0.7

env:
  - HOME=/
```

### Specified as a dictionary

```yaml
spec: v0.7

env:
  HOME: /
```

## Top-level `labels` attribute

A `labels` attribute CAN be specified which injects arbitrary key-value labels set during the packaging step:

```yaml
spec: v0.7

labels:
  key: value
```

Label keys must match the pattern `^[a-zA-Z0-9._/-]+$`.

## Top-level `volumes` attribute

A `volumes` attribute CAN be specified to declare the list of runtime mounts which are provided to the unikernel machine instance.

There are two forms of syntax: "short-hand" and "long-hand".
When specifying a destination path, this MUST be represented as an absolute path.

### Short-hand syntax

A source path on the host is mapped to a destination path in the unikernel using a colon (`:`) delimiter.
An optional third segment can specify a mode (e.g. `ro`):

```yaml
spec: v0.7

volumes:
  - ./src:/dest
  - ./data:/data:ro
```

When no colon is present, the entry is treated as a source mounted at `/`.

### Long-hand syntax

```yaml
spec: v0.7

volumes:
  - source: ./src
    destination: /dest
    driver: 9pfs
    readonly: false
```

The long-hand syntax supports the following fields:

| Field         | Type    | Description                     |
| ------------- | ------- | ------------------------------- |
| `source`      | string  | Path on the host                |
| `destination` | string  | Mount path inside the unikernel |
| `driver`      | string  | Filesystem driver (e.g. `9pfs`) |
| `readonly`    | boolean | Whether the volume is read-only |
| `mode`        | any     | Mode string (e.g. `ro`)         |

### Single volume as a string

The `volumes` attribute also accepts a single string value instead of a list:

```yaml
spec: v0.7

volumes: ./src:/dest
```

## Top-level `rootfs` attribute

The `rootfs` element CAN be specified to define the root filesystem.

### Short-hand syntax

When specified as a string, the source path is provided directly.
The output format defaults to `cpio`:

```yaml
spec: v0.7

rootfs: ./Dockerfile
```

The provided path can be one of the following:

- A path to an existing CPIO archive (initramfs file)
- A path to a directory which is then dynamically serialized into a filesystem image
- A path to a `Dockerfile` which will be constructed via BuildKit
- A path to a tarball (`.tar` or `.tar.gz`)

### Long-hand syntax

The long-hand syntax allows specifying additional attributes:

```yaml
spec: v0.7

rootfs:
  source: ./initramfs.erofs
  format: erofs
  type: dockerfile
```

The long-hand syntax supports the following fields:

| Field    | Type   | Description                                                        |
| -------- | ------ | ------------------------------------------------------------------ |
| `source` | string | **(required)** Path or reference to the filesystem source          |
| `format` | string | Output format of the filesystem image: `cpio` (default) or `erofs` |
| `type`   | string | Explicit source type (see below)                                   |

#### Source types

The `type` field can be used to explicitly declare what kind of source is being provided:

| Type         | Description                  |
| ------------ | ---------------------------- |
| `oci`        | An OCI image reference       |
| `dir`        | A directory containing files |
| `file`       | A single file                |
| `tarball`    | A tarball archive            |
| `cpio`       | An existing CPIO archive     |
| `erofs`      | An existing EROFS image      |
| `dockerfile` | A Dockerfile to be built     |

When `type` is not specified, the source type is inferred from the path.

#### Filesystem formats

Two output formats are supported:

- **`cpio`** (default) — produces a CPIO archive (initramfs)
- **`erofs`** — produces an EROFS filesystem image

## Top-level `roms` attribute

The `roms` attribute CAN be specified to declare additional read-only filesystem images to be provided to the unikernel at runtime.
Each entry follows the same syntax as `rootfs` (both short-hand and long-hand):

```yaml
spec: v0.7

roms:
  - ./extra-data/
  - source: ./assets.erofs
    format: erofs
    type: erofs
```

## Top-level `unikraft` attribute

The `unikraft` attribute CAN be specified and is used to define the source location of the Unikraft core which contains the main build system and core primitives for building a unikernel "from source".

If no `unikraft` element is specified, one of either `template` or `runtime` MUST otherwise be specified.

There are two forms of syntax: "short-hand" and "long-hand".

### Setting a specific version

```yaml
spec: v0.7

# Short-hand syntax
unikraft: stable

# Long-hand syntax
unikraft:
  version: stable
```

To specify a specific version of Unikraft, including a specific Git commit:

```yaml
spec: v0.7

# Short-hand for a specific version
unikraft: v0.14.0

# Short-hand for a specific commit
unikraft: 70bc0af
```

### Setting a specific source location

A remote fork, mirror, or local path can be set as the source:

```yaml
spec: v0.7

# Short-hand (uses HEAD of default branch)
unikraft: https://github.com/unikraft/unikraft.git

# With a specific branch or tag
unikraft: https://github.com/unikraft/unikraft.git@staging

# Long-hand syntax
unikraft:
  source: https://github.com/unikraft/unikraft.git
  version: staging
```

SSH-authenticated repositories are also supported:

```yaml
spec: v0.7

unikraft: ssh://git@github.com/unikraft/unikraft.git
```

A local path can be used:

```yaml
spec: v0.7

unikraft: path/to/unikraft
```

### Specifying KConfig configuration

To declare KConfig options, use the long-hand syntax.
KConfig values can be specified in list format or map format:

```yaml
spec: v0.7

# Using list-style formatting
unikraft:
  kconfig:
  - CONFIG_EXAMPLE=y

# Using map-style formatting
unikraft:
  kconfig:
    CONFIG_EXAMPLE: "y"
```

### A more complex example

All three sub-attributes — `source`, `version` and `kconfig` — can be combined:

```yaml
spec: v0.7

unikraft:
  source: https://github.com/unikraft/unikraft.git
  version: stable
  kconfig:
    CONFIG_EXAMPLE: "y"
```

## Top-level `runtime` attribute

The `runtime` attribute CAN be specified and is used to access a pre-built unikernel.
The value is specified as a string referencing an OCI image:

```yaml
spec: v0.7

runtime: unikraft.org/python3:latest
```

If no `runtime` element is specified, one of either `template` or `unikraft` MUST otherwise be specified.

The `runtime` element is useful when you do not need to build a unikernel from source — for example, when using an existing application runtime like NGINX, Redis, or a language runtime like Python3.

A typical pattern combines `runtime` with a filesystem and command:

```yaml
spec: v0.7

runtime: unikraft.org/python3:latest

volumes:
  - ./src:/src

cmd: ["/src/main.py"]
```

## Top-level `template` attribute

The `template` attribute CAN be specified to reference an external repository which contains an application based on another `Kraftfile`.
This offers a convenient mechanism for customizing or re-using configuration across multiple applications.

If no `template` element is specified, one of either `runtime` or `unikraft` MUST otherwise be specified.

### Short-hand syntax

```yaml
spec: v0.7

template: https://github.com/unikraft/app-elfloader.git
```

### Long-hand syntax

```yaml
spec: v0.7

template:
  source: https://github.com/unikraft/app-elfloader.git
  version: staging
```

The long-hand syntax supports the following fields:

| Field     | Type   | Description                            |
| --------- | ------ | -------------------------------------- |
| `source`  | string | Path or URL to the template repository |
| `version` | string | Branch, tag, or commit to use          |

### Template overlay behavior

The process of applying the template's `Kraftfile` on top of another uses an overlay mechanism.
Elements in the top-level `Kraftfile` overwrite the template's values when specified.

For example, given a template with:

```yaml
spec: v0.7

name: template

unikraft:
  version: stable
  kconfig:
    - CONFIG_LIBVFSCORE=y

targets:
  - qemu/x86_64
```

And a top-level `Kraftfile` referencing it:

```yaml
spec: v0.7

template: app/template:stable

unikraft:
  version: staging
```

The result after overlay is:

```yaml
spec: v0.7

name: template

unikraft:
  version: staging

targets:
  - qemu/x86_64
```

The merge behavior for each field is:

- **`name`**, **`targets`**, **`cmd`**, **`runtime`**, **`rootfs`**, **`roms`**, **`volumes`**: the overlay value completely replaces the template value when specified
- **`env`**, **`labels`**: maps are merged, with overlay values taking precedence for duplicate keys
- **`unikraft`**: `source` and `version` are replaced individually when specified; `kconfig` entries are merged with overlay values taking precedence
- **`libraries`**: merged by library name, with overlay entries replacing template entries for the same name
- **`template`**: not merged (avoids recursive templates)

## Top-level `libraries` attribute

Additional third-party libraries CAN be specified as part of the build and are listed in map format.
Similar to the `unikraft` attribute, each library can specify a `source`, `version` and a set of `kconfig` options:

```yaml
spec: v0.7

name: helloworld

unikraft: stable

libraries:
  # Short-hand syntax
  musl: stable

  # Long-hand syntax
  lwip:
    source: https://github.com/unikraft/lib-lwip.git
    version: stable
    kconfig:
      CONFIG_LWIP_TCP: "y"
      CONFIG_LWIP_SOCKET: "y"
```

## Top-level `targets` attribute

A target is defined as a specific destination that the resulting unikernel is destined for and consists at minimum of a specific platform (e.g. `qemu` or `firecracker`) and architecture (e.g. `x86_64` or `arm64`) tuple.

### Short-hand syntax

```yaml
spec: v0.7

targets:
  - qemu/x86_64
```

### Long-hand syntax

```yaml
spec: v0.7

targets:
  - plat: qemu
    arch: x86_64
```

The `architecture` and `platform` field names can be used as aliases for `arch` and `plat` respectively:

```yaml
spec: v0.7

targets:
  - platform: qemu
    architecture: x86_64
```

### Short-hand and long-hand can be mixed

```yaml
spec: v0.7

targets:
  - qemu/x86_64
  - plat: qemu
    arch: arm64
```

### Specifying KConfig per target

Each target can include its own `kconfig` options:

```yaml
spec: v0.7

unikraft: stable

targets:
  - plat: qemu
    arch: x86_64
    kconfig:
      CONFIG_LIBVFSCORE_AUTOMOUNT_ROOTFS: "y"
      CONFIG_LIBVFSCORE_ROOTFS_9PFS: "y"
      CONFIG_LIBVFSCORE_ROOTFS: "9pfs"
      CONFIG_LIBVFSCORE_ROOTDEV: "fs0"

  - plat: qemu
    arch: x86_64
    kconfig:
      CONFIG_LIBVFSCORE_AUTOMOUNT_ROOTFS: "y"
      CONFIG_LIBVFSCORE_ROOTFS_INITRD: "y"
      CONFIG_LIBVFSCORE_ROOTFS: "initrd"
```
