# script

A small line-based language for driving a Bubble Tea model through
`tuikit`. `testtui` in the CLI is built on it.

## Language

Each line is one command. Whitespace is flexible.

- Blank lines are ignored.
- `#` starts a comment (either as a whole line, or after a command).

### Commands

```
snapshot
sleep <time.Duration>
key <key>
wait <expr>
```

`time.Duration` is parsed by Go (e.g. `150ms`, `2s`, `1m`).

By default:

- `snapshot` prints the current view (ANSI is stripped).
- other commands are silent.

With `Executor.JSON`:

- every non-empty, non-comment line prints one JSON object
- `snapshot` includes the screen in the JSON event (and errors include the last screen)

## JSON output

With `Executor.JSON`, the output is newline-delimited JSON (JSONL): one object
per executed line.

Event shape:

```json
{
  "seq": 1,
  "line": 12,
  "cmd": "wait contains(\"Home\")",
  "kind": "wait",
  "elapsed_ms": 123,
  "snapshot": "... (only for snapshot) ...",
  "error": "... (only on errors) ..."
}
```

Notes:

- `seq` increments per executed line (comments/blank lines do not emit events).
- `line` is only set for a line that `Run` reads, not for one given to `RunLine` with 0.
- `snapshot` is present for the `snapshot` command.
- On failures, the event includes `error` and `snapshot` (the screen then).

### Wait expressions

Atoms:

- `contains("string")`

Operators:

- `not <expr>`
- `<expr> and <expr>`
- `<expr> or <expr>`
- parentheses: `(<expr>)`

Precedence is `not` > `and` > `or`.

Strings use double quotes and Go-style escapes (`\"`, `\n`, `\\`, ...).

Examples:

```
wait contains("Home > instances")
wait not contains("Loading...")
wait contains("alpha") and contains("bravo")
wait (not contains("ERROR")) or contains("No results")
```

### Keys

`key <key>` sends a Bubble Tea key press.

Supported named keys:

`enter`, `tab`, `backspace`, `esc`, `space`, `up`, `down`, `left`, `right`,
`pgup`, `pgdown`, `home`, `end`, `insert`, `delete`.

Modifiers are written with `+`:

`ctrl+<key>`, `alt+<key>`, `shift+<key>`, `meta+<key>`, `super+<key>`, `hyper+<key>`.

Examples:

```
key r
key enter
key tab
key shift+tab
key ctrl+c
key ?
```
