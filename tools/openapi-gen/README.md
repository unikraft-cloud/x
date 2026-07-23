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

`--package` is deprecated: it only works against specs produced by our
proto-based pipeline, which stamps schemas and operations with an `x-package`
extension. When set, only schemas and operations whose `x-package` matches
the given value are passed to templates, and the value is also exposed to
templates as the `x-package` variable (i.e. `{{ .Var "x-package" "" }}`).
Existing proto-sourced callers keep working, but new ones should use
`--tag`/`--namespace` below; `--package` will be removed once those callers
have migrated.

`--tag` and `--namespace` are the `x-package`-free equivalent, matching how
platform-api's TypeSpec compiler shapes its output: operations are tagged per
resource and models are named `Namespace.Type` (e.g. `Instances.Instance`).
`--tag` filters operations by their OpenAPI `tags`; `--namespace` filters
models by the prefix of their schema name. Since `Namespace.Type` isn't a
valid Go identifier, pair `--namespace` with `--namespace-flatten` to strip or
join the prefix once filtering is done. When exactly one `--namespace` is
given, its lowercased value is also exposed to templates as the
`current_package` variable, for distinguishing local from foreign type
references.

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
	Flatten:   "strip",
})
```

### Processing pipeline

1. **Parse** — Load the OpenAPI spec with `kin-openapi`, extract YAML property ordering from the raw document.
2. **Preprocess** — Hoist inline object/enum schemas (found via properties, composition, or array items) to top-level `components/schemas` entries, so every type a generator needs has a name.
3. **Filter/flatten** — Apply `--package`, `--tag`, and `--namespace` filtering, then `--namespace-flatten` to rewrite namespaced schema names into valid Go identifiers.
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
