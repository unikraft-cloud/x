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

| Flag          | Short | Description                                           |
| ------------- | ----- | ----------------------------------------------------- |
| `--input`     | `-i`  | Path, URL, or Git ref to the OpenAPI spec (required)  |
| `--output`    | `-o`  | Output directory for generated files (required)       |
| `--var`       | `-v`  | Set a template variable as `key=value` (repeatable)   |
| `--templates` | `-t`  | Directory or Git ref to template overrides (required) |

## Internals

### Processing pipeline

1. **Parse** — Load the OpenAPI spec with `kin-openapi`, extract YAML property ordering from the raw document.
2. **Preprocess** — Flatten `allOf` wrappers around `$ref`, promote inline object/enum schemas to top-level `components/schemas` entries, assign type names to inline enums.
3. **Generate** — Execute each template against a `TemplateData` value, `gofmt` the output, and write files.

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
| `propertyNamesOrdered`  | `schemaName, schema → []string` | Property names in YAML source order                            |
| `getProperty`           | `schema, name → *Schema`        | Get a property schema (traverses `allOf`)                      |
| `getPropertyRequired`   | `schema, name → bool`           | True if property is required (traverses `allOf`)               |
| `getTypePackage`        | `v → string`                    | Return `x-package` for a type ref (accepts `*Schema`, `*SchemaRef`, `*Parameter`, or `string`) |

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
Each template produces one output file named after the template.

A template can emit multiple files by using `---` section markers:

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

This produces `model.gen.go` (preamble), `model_variant_a.gen.go`, and `model_variant_b.gen.go`.

## License

See [LICENSE.md](../../LICENSE.md) for details.
