// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"strings"
	"testing"
	"testing/fstest"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderPack(t *testing.T, pack *Pack) map[string]string {
	t.Helper()
	g, err := NewGenerator(writeSpec(t), map[string]string{"package": "pkg"}, pack)
	require.NoError(t, err)
	files, err := g.Render()
	require.NoError(t, err)
	got := map[string]string{}
	for _, f := range files {
		got[f.Name] = string(f.Data)
	}
	return got
}

func tmpls(files ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for i := 0; i < len(files); i += 2 {
		fsys[files[i]] = &fstest.MapFile{Data: []byte(files[i+1])}
	}
	return fsys
}

func funcs(names ...string) func(*Spec) template.FuncMap {
	return func(*Spec) template.FuncMap {
		fm := template.FuncMap{}
		for _, name := range names {
			fm[name] = func() string { return name }
		}
		return fm
	}
}

func TestPackExtends(t *testing.T) {
	base := &Pack{
		FS: tmpls(
			"a.txt.tmpl", `{{ block "greeting" . }}hello{{ end }} {{ who }}`,
			"b.txt.tmpl", `base b`,
			"c.txt.tmpl", `base c`,
		),
		Funcs: funcs("who"),
	}
	derived := &Pack{
		Extends: base,
		FS: tmpls(
			"overrides.tmpl", `{{ define "greeting" }}hi{{ end }}`,
			"b.txt.tmpl", `derived b`,
		),
		Funcs: func(*Spec) template.FuncMap {
			return template.FuncMap{"who": func() string { return "derived" }}
		},
	}

	assert.Equal(t, map[string]string{
		"a.gen.txt": "hi derived",
		"b.gen.txt": "derived b",
		"c.gen.txt": "base c",
	}, renderPack(t, derived))

	assert.Equal(t, map[string]string{
		"a.gen.txt": "hello who",
		"b.gen.txt": "base b",
		"c.gen.txt": "base c",
	}, renderPack(t, base))
}

func TestPackImports(t *testing.T) {
	lib := &Pack{
		FS: tmpls(
			"lib.txt.tmpl", `{{ define "lib/name" }}{{ shout .SchemaName }}{{ end }}never rendered`,
			"main.txt.tmpl", `shares a file name with the importer`,
		),
		Funcs: func(*Spec) template.FuncMap {
			return template.FuncMap{"shout": strings.ToUpper}
		},
	}
	base := &Pack{Imports: []*Pack{lib}, FS: tmpls("base.txt.tmpl", `base`)}
	pack := &Pack{
		Extends: base,
		Imports: []*Pack{lib},
		FS:      tmpls("main.txt.tmpl", `{{ range .Models }}{{ template "lib/name" . }}{{ end }}`),
	}

	assert.Equal(t, map[string]string{
		"base.gen.txt": "base",
		"main.gen.txt": "WIDGET",
	}, renderPack(t, pack))
}

func TestPackFuncsSeeFilteredSpec(t *testing.T) {
	pack := &Pack{
		FS: tmpls("n.txt.tmpl", `{{ modelCount }} {{ pkg }}`),
		Funcs: func(s *Spec) template.FuncMap {
			return template.FuncMap{
				"modelCount": func() int { return len(s.Models()) },
				"pkg":        func() string { return s.Var("package", "") },
			}
		},
	}

	g, err := NewGenerator(writeSpec(t), map[string]string{"package": "pkg"}, pack)
	require.NoError(t, err)
	g.FilterByNamespace([]string{"Nothing"})

	files, err := g.Render()
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "0 pkg", string(files[0].Data))
}

func TestPackClashes(t *testing.T) {
	out := tmpls("out.txt.tmpl", `out`)

	for name, tc := range map[string]struct {
		pack *Pack
		err  string
	}{
		"func of two imports": {
			pack: &Pack{FS: out, Imports: []*Pack{
				{Name: "a", Funcs: funcs("f")},
				{Name: "b", Funcs: funcs("f")},
			}},
			err: `template func "f" is defined by both a and b`,
		},
		"func of an import": {
			pack: &Pack{
				Name:    "pack",
				FS:      out,
				Funcs:   funcs("f"),
				Imports: []*Pack{{Name: "lib", Funcs: funcs("f")}},
			},
			err: `template func "f" is defined by both lib and pack`,
		},
		"builtin func": {
			pack: &Pack{Name: "pack", FS: out, Funcs: funcs("pascalcase")},
			err:  `template func "pascalcase" is defined by both openapi-gen and pack`,
		},
		"define of an import": {
			pack: &Pack{
				Name:    "pack",
				FS:      tmpls("out.txt.tmpl", `{{ define "d" }}{{ end }}`),
				Imports: []*Pack{{Name: "lib", FS: tmpls("lib.tmpl", `{{ define "d" }}{{ end }}`)}},
			},
			err: `template "d" is defined by both lib:lib.tmpl and pack:out.txt.tmpl`,
		},
		"define of two imports": {
			pack: &Pack{FS: out, Imports: []*Pack{
				{Name: "a", FS: tmpls("a.tmpl", `{{ define "d" }}{{ end }}`)},
				{Name: "b", FS: tmpls("b.tmpl", `{{ define "d" }}{{ end }}`)},
			}},
			err: `template "d" is defined by both a:a.tmpl and b:b.tmpl`,
		},
		"define of an import of the base": {
			pack: &Pack{
				Name: "pack",
				FS:   out,
				Extends: &Pack{
					Name: "base",
					FS:   tmpls("base.tmpl", `{{ define "d" }}{{ end }}`),
				},
				Imports: []*Pack{{Name: "lib", FS: tmpls("lib.tmpl", `{{ define "d" }}{{ end }}`)}},
			},
			err: `template "d" is defined by both base:base.tmpl and lib:lib.tmpl`,
		},
		"two files of one pack": {
			pack: &Pack{Name: "pack", FS: tmpls(
				"x/out.txt.tmpl", `x`,
				"y/out.txt.tmpl", `y`,
			)},
			err: `template "out.txt.tmpl" is defined by both pack:x/out.txt.tmpl and pack:y/out.txt.tmpl`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewGenerator(writeSpec(t), nil, tc.pack)
			require.EqualError(t, err, tc.err)
		})
	}
}
