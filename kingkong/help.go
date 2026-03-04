// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2025, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package kingkong

import (
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"unikraft.com/x/colors"
	"unikraft.com/x/guesstermwidth"
)

const (
	DefaultColumnPadding = 4

	// NegatableDefault is a placeholder value for the Negatable tag to indicate
	// the negated flag is --no-<flag-name>. This is needed as at the time of
	// parsing a tag, the field's flag name is not yet known.
	NegatableDefault = "_"
)

var (
	Underline = lipgloss.NewStyle().Underline(true).Render
	Bold      = lipgloss.NewStyle().Bold(true).Render

	EnvVarColor     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: string(colors.Emerald500), Dark: string(colors.Emerald200)}).Render
	CommandColor    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: string(colors.Blue500), Dark: string(colors.Blue200)}).Render
	DimmedColor     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: string(colors.Slate500), Dark: string(colors.Slate300)}).Render
	DimmedMoreColor = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: string(colors.Slate400), Dark: string(colors.Slate400)}).Render
)

// HelpPrinter returns a function implementation of kong.HelpPrinter.
//
// Usage:
//
// ```go
// kong.Help(kingkong.HelpPrinter("v1.0.0"))
// ````
func HelpPrinter(version string) func(options kong.HelpOptions, ctx *kong.Context) error {
	return func(options kong.HelpOptions, ctx *kong.Context) error {
		if ctx.Empty() {
			options.Summary = false
		}

		w := newHelpWriter(ctx, options)
		selected := ctx.Selected()
		if selected == nil {
			printApp(version, w, ctx.Model)
		} else {
			printCommand(w, ctx.Model, selected)
		}

		return w.Write(ctx.Stdout)
	}
}

func Summary(app *kong.Node) string {
	summary := ""

	switch app.Type {
	case kong.ApplicationNode, kong.CommandNode:
		summary += app.Name
	case kong.ArgumentNode:
		summary += "<" + app.Name + ">"
	}
	parent := app.Parent
	for parent != nil {
		summary = parent.Name + " " + summary
		parent = parent.Parent
	}

	if flags := app.FlagSummary(true); flags != "" {
		summary += " " + flags
	}
	args := []string{}
	optional := 0
	for _, arg := range app.Positional {
		var argSummary string

		if arg.Flag != nil {
			if arg.IsBool() {
				argSummary = fmt.Sprintf("--%s", arg.Name)
			} else {
				argSummary = fmt.Sprintf("--%s=%s", arg.Name, arg.Flag.FormatPlaceHolder())
			}
		} else {
			argSummary = "<" + arg.Name + ">"
			if arg.IsCumulative() {
				argSummary += " ..."
			}
			if !arg.Required {
				argSummary = "[" + argSummary + "]"
			}

			if arg.Tag.Optional {
				optional++
				argSummary = strings.TrimRight(argSummary, "]")
			}
		}

		args = append(args, DimmedColor(argSummary))
	}
	if len(args) != 0 {
		summary += " " + strings.Join(args, " ") + DimmedColor(strings.Repeat("]", optional))
	} else if len(app.Children) > 0 {
		summary += " " + CommandColor("<command>")
	}
	allFlags := app.Flags
	if app.Parent != nil {
		allFlags = append(allFlags, app.Parent.Flags...)
	}
	for _, flag := range allFlags {
		if !flag.Required {
			summary += " " + DimmedColor("[flags]")
			break
		}
	}
	return summary
}

func printApp(version string, w *helpWriter, app *kong.Application) {
	printNodeHelp(w, app.Node)
	if !w.NoAppSummary {
		w.Print(Underline("Usage") + ":")
		w.Indent().Printf("%s\n", Summary(app.Node))
	}

	w.Print(Underline("Version") + ":")
	w.Indent().Printf("%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)

	printNodeDetail(w, app.Node, true)

	cmds := app.Leaves(true)
	if len(cmds) > 0 && app.HelpFlag != nil {
		w.Print("")
		if w.Summary {
			w.Printf(`Run "%s --help" for more information.`, app.Name)
		} else {
			w.Printf(`Run "%s <command> --help" for more information on a command.`, app.Name)
		}
	}
}

func printCommand(w *helpWriter, app *kong.Application, cmd *kong.Command) {
	printNodeHelp(w, cmd)
	if !w.NoAppSummary {
		w.Print(Underline("Usage") + ":")
		w.Indent().Printf("%s", Summary(cmd))
	}

	printNodeDetail(w, cmd, true)

	if w.Summary && app.HelpFlag != nil {
		w.Print("")
		w.Printf(`Run "%s --help" for more information.`, cmd.FullPath())
	}
}

func printNodeDetail(w *helpWriter, node *kong.Node, hide bool) {
	if w.Summary {
		return
	}

	if node.Detail != "" {
		w.Print("")
		w.Wrap(node.Detail)
	}

	if len(node.Positional) > 0 {
		w.Print("")
		w.Print(Underline("Arguments") + ":")
		writePositionals(w.Indent(), node.Positional)
	}

	printFlags := func() {
		if flags := node.AllFlags(true); len(flags) > 0 {
			groupedFlags := GroupFlags(flags)
			for _, group := range groupedFlags {
				w.Print("")
				if group.Metadata.Title != "" {
					w.Wrap(group.Metadata.Title)
				}
				if group.Metadata.Description != "" {
					w.Indent().Wrap(group.Metadata.Description)
					w.Print("")
				}
				writeFlags(w.Indent(), group.Flags)
			}
		}
	}

	if !w.FlagsLast {
		printFlags()
	}

	var cmds []*kong.Node
	if w.NoExpandSubcommands {
		cmds = node.Children
	} else {
		cmds = node.Leaves(hide)
	}

	if len(cmds) > 0 {
		iw := w.Indent()
		if w.Tree {
			w.Print("")
			w.Print(Underline("Commands") + ":")
			writeCommandTree(iw, node)
		} else {
			groupedCmds := GroupCommands(cmds)
			for _, group := range groupedCmds {
				w.Print("")
				if group.Metadata.Title != "" {
					w.Wrap(group.Metadata.Title)
				}
				if group.Metadata.Description != "" {
					w.Indent().Wrap(group.Metadata.Description)
					w.Print("")
				}

				if w.Compact {
					writeCompactCommandList(group.Commands, iw)
				} else {
					writeCommandList(group.Commands, iw)
				}
			}
		}
	}

	if examples := getExamples(node); len(examples) > 0 {
		w.Print("")
		w.Print(Underline("Examples") + ":")

		comment := &helpWriter{
			indent:      DimmedMoreColor(w.indent + "  # "),
			lines:       w.lines,
			width:       w.width - 4,
			HelpOptions: w.HelpOptions,
		}

		for i, example := range examples {
			for _, line := range strings.Split(strings.TrimSpace(ansi.Wrap(strings.TrimSpace(example.Description), comment.width, "-")), "\n") {
				comment.Print(DimmedMoreColor(line))
			}
			for _, command := range example.Commands {
				w.Indent().Wrap(command)
			}
			if i != len(examples)-1 {
				w.Print("")
			}
		}
	}

	for _, section := range getAdditionalSections(node) {
		w.Print("")
		w.Print(Underline(section.Title) + ":")
		w.Indent().Wrap(section.Content)
	}

	if w.FlagsLast {
		printFlags()
	}
}

func printNodeHelp(w *helpWriter, node *kong.Node) {
	if node.Help == "" {
		return
	}
	if len(*w.lines) > 0 {
		w.Print("")
	}
	w.Wrap(node.Help)
	w.Print("")
}

func writeCommandList(cmds []*kong.Node, iw *helpWriter) {
	for i, cmd := range cmds {
		if cmd.Hidden {
			continue
		}
		printCommandSummary(iw, cmd)
		if i != len(cmds)-1 {
			iw.Print("")
		}
	}
}

func writeCompactCommandList(cmds []*kong.Node, iw *helpWriter) {
	rows := [][2]string{}
	for _, cmd := range cmds {
		if cmd.Hidden {
			continue
		}

		var buf strings.Builder

		switch cmd.Type {
		case kong.CommandNode:
			// Show the default command name first and remove any aliases which are
			// equal to it.
			buf.WriteString(
				strings.Join(
					append(
						[]string{cmd.Name},
						slices.DeleteFunc(
							cmd.Aliases,
							func(alias string) bool {
								return alias == cmd.Name
							},
						)...,
					),
					", ",
				),
			)
		case kong.ArgumentNode:
			buf.WriteString("<")
			buf.WriteString(cmd.Name)
			buf.WriteString(">")
		default:
		}

		rows = append(rows, [2]string{CommandColor(buf.String()), DimmedColor(cmd.Help)})
	}

	writeTwoColumns(iw, rows)
}

func writeCommandTree(w *helpWriter, node *kong.Node) {
	rows := make([][2]string, 0, len(node.Children)*2)
	for i, cmd := range node.Children {
		if cmd.Hidden {
			continue
		}
		rows = append(rows, w.CommandTree(cmd, "")...)
		if i != len(node.Children)-1 {
			rows = append(rows, [2]string{"", ""})
		}
	}
	writeTwoColumns(w, rows)
}

func printCommandSummary(w *helpWriter, cmd *kong.Command) {
	w.Print(cmd.Summary())
	if cmd.Help != "" {
		w.Indent().Wrap(cmd.Help)
		w.Print("")
	}
}

type helpWriter struct {
	indent string
	width  int
	lines  *[]string
	kong.HelpOptions
}

func newHelpWriter(ctx *kong.Context, options kong.HelpOptions) *helpWriter {
	lines := []string{}
	wrapWidth := guesstermwidth.GuessTermWidth(ctx.Stdout)
	if options.WrapUpperBound > 0 && wrapWidth > options.WrapUpperBound {
		wrapWidth = options.WrapUpperBound
	}
	w := &helpWriter{
		indent:      "",
		width:       wrapWidth,
		lines:       &lines,
		HelpOptions: options,
	}
	return w
}

func (h *helpWriter) Printf(format string, args ...any) {
	h.Print(fmt.Sprintf(format, args...))
}

func (h *helpWriter) Print(text string) {
	*h.lines = append(*h.lines, strings.TrimRight(h.indent+text, " "))
}

// Indent returns a new helpWriter indented by two characters.
func (h *helpWriter) Indent() *helpWriter {
	return &helpWriter{indent: h.indent + "  ", lines: h.lines, width: h.width - 2, HelpOptions: h.HelpOptions}
}

func (h *helpWriter) String() string {
	return strings.Join(*h.lines, "\n")
}

func (h *helpWriter) Write(w io.Writer) error {
	for _, line := range *h.lines {
		_, err := io.WriteString(w, line+"\n")
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *helpWriter) Wrap(text string) {
	for _, line := range strings.Split(strings.TrimSpace(ansi.Wrap(strings.TrimSpace(text), h.width, "-")), "\n") {
		h.Print(line)
	}
}

// helpValueFormatter implements kong.HelpValueFormatter.
func helpValueFormatter(value *kong.Value) string {
	var buf strings.Builder

	// Ensure help text ends with a period.
	buf.WriteString(DimmedColor(strings.TrimSuffix(value.Help, ".") + "."))
	buf.WriteString("\n")

	if len(value.Default) > 0 {
		buf.WriteString(DimmedColor("[default: ") + value.Default)
	}

	if len(value.Tag.Enum) > 0 {
		buf.WriteString(DimmedColor(", choice: ") + value.Tag.Enum)
	}

	if len(value.Default) > 0 || len(value.Tag.Enum) > 0 {
		buf.WriteString(DimmedColor("]"))
	}

	return buf.String()
}

func writePositionals(w *helpWriter, args []*kong.Positional) {
	rows := [][2]string{}
	for _, arg := range args {
		rows = append(rows, [2]string{arg.Summary(), helpValueFormatter(arg)})
	}
	writeTwoColumns(w, rows)
}

func writeFlags(w *helpWriter, groups [][]*kong.Flag) {
	rows := [][2]string{}
	for i, group := range groups {
		if i > 0 {
			rows = append(rows, [2]string{"", ""})
		}
		for _, flag := range group {
			if !flag.Hidden {
				rows = append(rows, [2]string{formatFlag(flag), helpValueFormatter(flag.Value)})
			}
		}
	}
	writeTwoColumns(w, rows)
}

func writeTwoColumns(w *helpWriter, rows [][2]string) {
	for _, row := range rows {
		w.Printf("%s", row[0])

		lines := strings.Split(strings.TrimRight(row[1], "\n"), "\n")
		for _, line := range lines {
			w.Printf("%s%s", strings.Repeat(" ", DefaultColumnPadding*2), line)
		}
	}
}

// formatFlag returns a formatted flag string, including short and long names,
func formatFlag(flag *kong.Flag) string {
	var buf strings.Builder
	names := append([]string{flag.Name}, flag.Aliases...)
	isBool := flag.IsBool()

	short := "    "
	if flag.Short != 0 {
		short = "-" + string(flag.Short) + DimmedColor(", ")
	}

	for i := range names {
		if isBool && flag.Tag.Negatable == NegatableDefault {
			names[i] = "[no-]" + names[i]
		} else if isBool && flag.Tag.Negatable != "" {
			names[i] += "/" + flag.Tag.Negatable
		}
		names[i] = "--" + names[i]
	}

	buf.WriteString(fmt.Sprintf("%s%s", short, strings.Join(names, ", ")))

	if len(flag.Tag.Envs) > 0 {
		buf.WriteString(" ")
		buf.WriteString(DimmedColor("("))
		buf.WriteString(formatEnvs(flag.Tag.Envs))
		buf.WriteString(DimmedColor(")"))
	}

	return buf.String()
}

func formatEnvs(envs []string) string {
	formatted := make([]string, len(envs))
	for i := range envs {
		formatted[i] = EnvVarColor("$" + envs[i])
	}

	return strings.Join(formatted, ", ")
}

func getExamples(node *kong.Node) []Example {
	help, ok := node.Target.Interface().(ExamplesProvider)
	if !ok {
		return nil
	}
	return help.Examples()
}

func getAdditionalSections(node *kong.Node) []HelpSection {
	help, ok := node.Target.Interface().(AdditionalHelp)
	if !ok {
		return nil
	}
	return help.HelpSections()
}
