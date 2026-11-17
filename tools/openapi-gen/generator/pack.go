// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package generator

import (
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"text/template"

	"github.com/getkin/kin-openapi/openapi3"

	"unikraft.com/x/tools/openapi-gen/internal/openapi"
	"unikraft.com/x/tools/openapi-gen/spec"
)

// Pack is a set of templates together with the template functions they need.
//
// Every *.tmpl file in a pack's FS is an output template, rendering one file
// (or several, via "---" section markers). A pack may build on others:
//
//   - Imports contribute their define blocks and functions, but none of their
//     output templates.
//   - Extends contributes all of that and its output templates too. A file of
//     the same name in this pack replaces the inherited one, and a define
//     replaces an inherited block of the same name.
//
// Any other name defined twice, whether a function, a define or a file, is an
// error.
type Pack struct {
	// Name identifies the pack in error messages.
	Name string

	FS      fs.FS
	Funcs   func(*Spec) template.FuncMap
	Imports []*Pack
	Extends *Pack
}

// Spec is the view of the parsed document that a Pack's Funcs are bound to.
// Its methods reflect any filtering and flattening applied after the
// generator is created, so call them from inside the returned functions.
type Spec struct {
	g *Generator
}

// Doc returns the parsed OpenAPI document.
func (s *Spec) Doc() *openapi3.T { return s.g.parser.Doc() }

// Models returns the models passed to templates.
func (s *Spec) Models() []spec.Model { return s.g.models }

// Operations returns the operations passed to templates.
func (s *Spec) Operations() []spec.PathOperation { return s.g.operations }

// Var returns the template variable key, or fallback when it is unset.
func (s *Spec) Var(key, fallback string) string {
	if v, ok := s.g.vars[key]; ok {
		return v
	}
	return fallback
}

type origin struct {
	pack *Pack
	from string
}

type names map[string]origin

// add merges src into n. A name that n already holds from another pack is a
// clash, unless n got it from base: what the pack inherits through Extends.
func (n names) add(src names, base names, kind string) error {
	for name, o := range src {
		cur, ok := n[name]
		if b, inherited := base[name]; !ok || cur.pack == o.pack || (inherited && b == cur) {
			n[name] = o
			continue
		}
		return fmt.Errorf("%s %q is defined by both %s and %s", kind, name, cur.from, o.from)
	}
	return nil
}

var builtin = &Pack{Name: "openapi-gen"}

func (p *Pack) describe(file string) string {
	name := p.Name
	if name == "" {
		name = "unnamed pack"
	}
	if file == "" {
		return name
	}
	return name + ":" + file
}

type packLoader struct {
	spec *Spec
	ids  map[*Pack]int
}

func (l *packLoader) id(p *Pack) int {
	if id, ok := l.ids[p]; ok {
		return id
	}
	l.ids[p] = len(l.ids)
	return l.ids[p]
}

func (l *packLoader) funcs(p *Pack) (names, template.FuncMap, error) {
	n, fm := names{}, template.FuncMap{}
	var base names
	if p.Extends != nil {
		bn, bfm, err := l.funcs(p.Extends)
		if err != nil {
			return nil, nil, err
		}
		n, fm, base = bn, bfm, maps.Clone(bn)
	}
	for _, imp := range p.Imports {
		in, ifm, err := l.funcs(imp)
		if err != nil {
			return nil, nil, err
		}
		if err := n.add(in, nil, "template func"); err != nil {
			return nil, nil, err
		}
		maps.Copy(fm, ifm)
	}
	if p.Funcs != nil {
		own := p.Funcs(l.spec)
		on := names{}
		for name := range own {
			on[name] = origin{pack: p, from: p.describe("")}
		}
		if err := n.add(on, base, "template func"); err != nil {
			return nil, nil, err
		}
		maps.Copy(fm, own)
	}
	return n, fm, nil
}

type packFile struct {
	origin
	tmpl   *template.Template
	output bool
}

type packTemplates struct {
	names names
	files []packFile
}

func (l *packLoader) templates(p *Pack, funcs template.FuncMap, output bool) (*packTemplates, error) {
	t := &packTemplates{names: names{}}
	var base names
	if p.Extends != nil {
		bt, err := l.templates(p.Extends, funcs, output)
		if err != nil {
			return nil, err
		}
		t, base = bt, maps.Clone(bt.names)
	}
	for _, imp := range p.Imports {
		it, err := l.templates(imp, funcs, false)
		if err != nil {
			return nil, err
		}
		if err := t.names.add(it.names, nil, "template"); err != nil {
			return nil, err
		}
		t.files = append(t.files, it.files...)
	}
	if p.FS == nil {
		return t, nil
	}

	paths, err := findTemplateFiles(p.FS)
	if err != nil {
		return nil, err
	}
	own := names{}
	for _, file := range paths {
		data, err := fs.ReadFile(p.FS, file)
		if err != nil {
			return nil, fmt.Errorf("reading template %s: %w", p.describe(file), err)
		}
		name := path.Base(file)
		if !output {
			name = fmt.Sprintf("import/%d/%s", l.id(p), file)
		}
		tmpl, err := template.New(name).Funcs(funcs).Parse(string(data))
		if err != nil {
			return nil, fmt.Errorf("loading template %s: %w", p.describe(file), err)
		}

		o := origin{pack: p, from: p.describe(file)}
		fileNames := names{}
		for _, def := range tmpl.Templates() {
			fileNames[def.Name()] = o
		}
		for name := range fileNames {
			if cur, ok := own[name]; ok {
				return nil, fmt.Errorf("template %q is defined by both %s and %s", name, cur.from, o.from)
			}
			own[name] = o
		}
		t.files = append(t.files, packFile{origin: o, tmpl: tmpl, output: output})
	}
	if err := t.names.add(own, base, "template"); err != nil {
		return nil, err
	}
	return t, nil
}

func (g *Generator) loadPack(p *Pack) error {
	l := &packLoader{spec: &Spec{g: g}, ids: map[*Pack]int{}}

	builtins := openapi.Funcs(g.parser, &g.models, &g.vars)
	n := names{}
	for name := range builtins {
		n[name] = origin{pack: builtin, from: builtin.Name}
	}
	pn, pfm, err := l.funcs(p)
	if err != nil {
		return err
	}
	if err := n.add(pn, nil, "template func"); err != nil {
		return err
	}
	funcs := maps.Clone(builtins)
	maps.Copy(funcs, pfm)

	pt, err := l.templates(p, funcs, true)
	if err != nil {
		return err
	}

	tmpl := template.New("").Funcs(funcs)
	var outputs []string
	type key struct {
		origin
		name string
	}
	seen := map[key]bool{}
	for _, f := range pt.files {
		k := key{f.origin, f.tmpl.Name()}
		if seen[k] {
			continue
		}
		seen[k] = true
		for _, def := range f.tmpl.Templates() {
			if _, err := tmpl.AddParseTree(def.Name(), def.Tree); err != nil {
				return fmt.Errorf("loading template %s: %w", f.from, err)
			}
		}
		if name := f.tmpl.Name(); f.output && !slices.Contains(outputs, name) {
			outputs = append(outputs, name)
		}
	}
	if len(outputs) == 0 {
		return fmt.Errorf("no templates found")
	}

	g.templates = tmpl
	g.templateNames = outputs
	return nil
}
