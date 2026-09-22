# `openapi-gen`

A code generator which reads and parses OpenAPI 3.0 specification to generate code based on [Go templates](https://pkg.go.dev/text/template).

## Usage

```sh
go run unikraft.com/x/tools/openapi-gen@latest \
  -i openapi.yaml \
  -o ./gen \
  -v package=myapi \
  -t ./templates/go-client
```

The `--input` flag also accepts an HTTP(S) URL:

```sh
go run unikraft.com/x/tools/openapi-gen@latest \
  -i https://example.com/openapi.yaml \
  -o ./gen \
  -s package=myapi \
  -t ./templates/go-client
```

Or a Git repository reference (cloned via SSH, falling back to HTTPS):

```sh
go run unikraft.com/x/tools/openapi-gen@latest \
  -i github.com/org/repo@main#file=path/to/openapi.yaml \
  -o ./gen \
  -s package=myapi \
  -t ./templates/go-client
```

The `--templates` flag likewise accepts a Git repository reference using `#dir=` to point at a directory:

```sh
go run unikraft.com/x/tools/openapi-gen@latest \
  -i openapi.yaml \
  -o ./gen \
  -s package=myapi \
  -t github.com/org/repo@main#dir=templates/go-client
```

| Flag                 | Short | Description                                                                |
| -------------------- | ----- | --------------------------------------------------------------------------|
| `--input`            | `-i`  | Path, URL, or Git ref to the OpenAPI spec (required)                      |
| `--output`           | `-o`  | Output directory for generated files (required)                           |
| `--var`              | `-v`  | Set a template variable as `key=value` (repeatable)                       |
| `--templates`        | `-t`  | Directory or Git ref to template overrides (required)                    |
| `--package`          |       | Deprecated: use `--tag`/`--namespace` instead. Filter to schemas/operations whose `x-package` matches this value |
| `--tag`              |       | Filter to operations carrying one of these tags (repeatable)              |
| `--namespace`        |       | Filter to schemas in one of these namespaces, e.g. `Instances` (repeatable) |
| `--namespace-flatten`|       | Rewrite namespaced schema names: `strip` drops the prefix, `join` concatenates segments |
| `--namespace-package`|       | Namespace whose schemas live in another Go package, as `<namespace>[=<package>]` (repeatable) |

## Selecting what to generate

One OpenAPI document usually holds more than one Go package's worth of types.
These flags decide which part of it a run emits, and how it refers to the rest.

### `--tag`

Keeps only the operations carrying one of the given tags, and is repeatable.
Models are untouched. TypeSpec tags each operation with its resource, so a tag
is one resource's worth of endpoints:

```sh
openapi-gen -i api.yaml -o ./gen -v package=instancesv1 -t ./templates/go-server \
  --tag=Instances --tag=Volumes
```

### `--namespace`

Keeps only the models in one of the given namespaces, and is repeatable. A
model's namespace is the first segment of its schema name, so
`Instances.Instance` is in `Instances` and `Org.Common.Error` is in `Org`.
Models with no namespace at all are dropped.

TypeSpec's OpenAPI3 emitter names a document's own schemas without a prefix
and prefixes only the types imported from another namespace. `--namespace` is
therefore for generating an imported namespace on its own, not a service's own
models — a service run selects its operations with `--tag` and lets every
unnamespaced model through.

When exactly one namespace is given, its lowercased value is exposed to
templates as the `current_package` variable.

### `--namespace-flatten`

`Namespace.Type` is not a valid Go identifier. `strip` rewrites it to `Type`,
`join` to `NamespaceType`, and the default leaves it alone. Flattening runs
after every filter, so the filters still match the original names.

### `--namespace-package`

Marks a namespace as belonging to another Go package: its schemas are not
generated in this run, and every reference to one is qualified with that
package. The value is `<namespace>[=<package>]`, and the package defaults to
the namespace's last segment lowercased, so `--namespace-package=Org.Common`
and `--namespace-package=Org.Common=common` mean the same thing. Pass the flag
more than once for more than one namespace.

The `go-server` templates render a qualified reference as `<package>v1.Type`
and import `<base_package>/<package>/v1`, where `base_package` is the template
variable of that name.

### Generating a shared package

Several documents that import the same namespace can share one Go package for
it instead of each generating a copy. Generate the shared package first,
selecting the namespace and nothing else:

```sh
openapi-gen -i api.yaml -o ./api/common/v1 -v package=commonv1 \
  -v base_package=github.com/org/repo/api -t ./templates/go-server \
  --tag=NoOperations --namespace=Org --namespace-flatten=strip
```

`--tag=NoOperations` matches no tag, which is how a models-only run asks for no
handlers.

Then generate each consumer, mapping that namespace to the shared package:

```sh
openapi-gen -i api.yaml -o ./api/instances/v1 -v package=instancesv1 \
  -v base_package=github.com/org/repo/api -t ./templates/go-server \
  --tag=Instances --namespace-package=Org.Common --namespace-flatten=strip
```

The consumer emits its own models plus handlers for the `Instances` tag, and
its references to `Org.Common.*` come out as `commonv1.Error` with an import of
`github.com/org/repo/api/common/v1`.

### `--package` (deprecated)

Filters schemas and operations to those whose `x-package` extension matches the
given value, and exposes it to templates as the `x-package` variable (i.e.
`{{ .Var "x-package" "" }}`). Only our proto-based pipeline emits that
extension. Existing proto-sourced callers keep working; new ones should use the
flags above. `--package` will be removed once those callers have migrated.

## Internals

### Go SDK

The OpenAPI generator can be used as a package:

```golang
import "unikraft.com/x/tools/openapi-gen/generator"

err := generator.Run(generator.Options{
	Input:     "path/to/openapi.yaml",
	Output:    "path/to/output/directory",
	Var:       map[string]string{
		"var1": "value1",
		"var2": "value2",
	},
	Templates: "path/to/templates/directory",
	Package:   "package-name",
	Tag:       []string{"tag1", "tag2"},
	Namespace: []string{"namespace-name"},
	NamespacePackage: map[string]string{
		"Org.Common": "common",
	},
	Flatten:   "strip",
})
```

### Processing pipeline

1. **Parse** — Load the OpenAPI spec with `kin-openapi`, extract YAML property ordering from the raw document.
2. **Preprocess** — Hoist inline object/enum schemas (found via properties, composition, or array items) to top-level `components/schemas` entries, so every type a generator needs has a name.
3. **Filter/flatten** — Apply `--package`, `--tag`, and `--namespace` filtering, drop the models `--namespace-package` assigns elsewhere, then `--namespace-flatten` to rewrite namespaced schema names into valid Go identifiers.
4. **Generate** — Execute each template against a `TemplateData` value, `gofmt` the output, and write files.

### Template data

Every template receives a single `TemplateData` value:

```go
type TemplateData struct {
   Operations []PathOperation
   Models     []Model
}
```

Templates access user-supplied variables via the `.Var` method:

```
{{ .Var "package" "defaultpkg" }}
```

The first argument is the variable name (matching a `-s key=value` flag), the second is the fallback value returned when the key was not set.
The `.Var` method is available on both `TemplateData` (top-level) and `PathOperation` (inside `define` blocks).

`PathOperation` pairs an `*openapi3.Operation` with its HTTP path and method. `Model` pairs a schema name with its `*openapi3.Schema`.

Operations are sorted by tag then by operation ID.
Models are sorted alphabetically by schema name.

## Template functions

Templates have access to all [Sprig](https://masterminds.github.io/sprig/) functions plus the following:

### Case conversion

| Function             | Input       | Output      |
| -------------------- | ----------- | ----------- |
| `pascalcase`         | `"foo_bar"` | `"FooBar"`  |
| `camelcase`          | `"foo_bar"` | `"fooBar"`  |
| `snakecase`          | `"FooBar"`  | `"foo_bar"` |
| `kebabcase`          | `"FooBar"`  | `"foo-bar"` |
| `screamingsnakecase` | `"FooBar"`  | `"FOO_BAR"` |

### Type helpers

| Function         | Signature                           | Description                                            |
| ---------------- | ----------------------------------- | ------------------------------------------------------ |
| `schemaToGoType` | `schema → string`                   | Convert an OpenAPI schema to a Go type                 |
| `paramToGoType`  | `param → string`                    | Convert an OpenAPI parameter to a Go type              |
| `refName`        | `ref → string`                      | Extract type name from a `$ref` string                 |
| `getType`        | `schema → string`                   | Return the OpenAPI type string (nil-safe)              |
| `enumBaseGoType` | `schema → string`                   | Underlying Go type for an enum (`string`, `int`, etc.) |
| `enumValue`      | `schema, val → string`              | Format an enum constant value (quoted for strings)     |
| `inlineEnums`    | `schemaName, schema → []inlineEnum` | Collect inline enum properties from a struct schema    |

### Property helpers

| Function               | Signature                       | Description                                                    |
| ---------------------- | ------------------------------- | -------------------------------------------------------------- |
| `propertyNamesOrdered`  | `schemaName, schema → []string` | Property names in YAML source order, falling back to sorted composition order |
| `getProperty`           | `schema, name → *Schema`        | Get a property schema (traverses `allOf`/`oneOf`/`anyOf`)      |
| `getPropertyRequired`   | `schema, name → bool`           | True if property is required (traverses `allOf`)               |
| `getTypePackage`        | `v → string`                    | Deprecated (proto-only): return `x-package` for a type ref (accepts `*Schema`, `*SchemaRef`, `*Parameter`, or `string`) |

### Iteration helpers

These return sorted slices for deterministic output:

| Function              | Signature                     | Description                               |
| --------------------- | ----------------------------- | ----------------------------------------- |
| `uniqueTags`          | `operations → []string`       | Deduplicated, sorted tags from operations |
| `sortedResponseCodes` | `responses → []ResponseEntry` | Response entries sorted by status code    |
| `sortedContentTypes`  | `content → []ContentEntry`    | Content entries sorted by media type      |

### Text helpers

| Function         | Signature                      | Description                                    |
| ---------------- | ------------------------------ | ---------------------------------------------- |
| `capitalize`     | `string → string`              | Uppercase first letter                         |
| `goSafeName`     | `string → string`              | Prefix Go reserved words with `_`              |
| `wrapComment`    | `text, width, prefix → string` | Word-wrap with prefix on continuation lines    |

## Custom templates

Create a directory with `.tmpl` files and pass it via `--templates`.

### Output filenames

By default each template produces a single output file whose name is derived
from the template filename:

- The trailing `.tmpl` suffix is stripped.
- If the result does not already contain `.gen`, it is inserted before the
  file extension (e.g. `model.go.tmpl` → `model.gen.go`, `notes.tmpl` →
  `notes.gen`).

This keeps generated files easy to identify and to exclude from tooling.

### Multi-file output via `---` markers

A template can emit multiple files by using `---` section markers. The
filename after `---` is used verbatim (no `.gen` insertion), so include the
extension you want:

```
{{ /* preamble goes to the base file */ }}
package {{ .Var "package" "main" }}
--- model_variant_a.gen.go
package {{ .Var "package" "main" }}
// variant_a content
--- model_variant_b.gen.go
package {{ .Var "package" "main" }}
// variant_b content
```

This produces `model.gen.go` (preamble, named via the default rule above),
`model_variant_a.gen.go`, and `model_variant_b.gen.go`. Section filenames
may include subdirectories (e.g. `subdir/foo.gen.go`); parent directories
are created automatically. Absolute paths and paths that escape the output
directory are rejected.

## License

See [LICENSE.md](../../LICENSE.md) for details.
