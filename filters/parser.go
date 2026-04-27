// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026, The containerd Authors.
// Licensed under the Apache License, Version 2.0 (the "License").
// You may not use this file except in compliance with the License.

package filters

import (
	"fmt"
	"io"
	"regexp"
)

/*
Parse the strings into a filter that may be used with an adaptor.

The filter is made up of zero or more selectors.

The format is a comma separated list of expressions, in the form of
`<fieldpath><op><value>`, known as selectors. All selectors must match the
target object for the filter to be true.

We define the following operators:

  - "==" (or "=") for equality
  - "!=" (or "!==") for not equal
  - "~=" for a regular expression match
  - "!~=" for a negated regular expression match

If the operator and value are not present, the matcher will test for the
presence of a value, as defined by the target object.

A wildcard "*" may appear in a field path to iterate over all entries of a
map or array field. Wildcards can be chained (e.g. "*.*") and may be followed
by further sub-field paths (e.g. "authors.*.email"). With positive operators
(==, ~=), the wildcard matches if any entry satisfies the condition. With
negated operators (!=, !~=), it matches only if all entries satisfy the
condition.

Values after "~=" or "!~=" may be quoted with "/" or "|" in addition to
double quotes, which is convenient for patterns containing quotes or
backslashes (e.g. name~=/[abc]{0,2}/ or path~=|foo/bar|).

The formal grammar is as follows:

	selectors := selector ("," selector)*
	selector  := fieldpath (operator value)?
	           | fieldpath? "*" ("." selector | operator value)?
	fieldpath := field ("." field)*
	field     := quoted | [A-Za-z] [A-Za-z0-9_]+
	operator  := "=" | "==" | "!=" | "!==" | "~=" | "!~="
	value     := quoted | regexp-quoted | [^\s,]+
	quoted    := <go string syntax>
	regexp-quoted := "/" ... "/" | "|" ... "|"
*/
func Parse(s string) (Filter, error) {
	// special case empty to match all
	if s == "" {
		return Always, nil
	}

	p := parser{input: s}
	return p.parse()
}

// ParseAll parses each filter in ss and returns a filter that will return true
// if any filter matches the expression.
//
// If no filters are provided, the filter will match anything.
func ParseAll(ss ...string) (Filter, error) {
	if len(ss) == 0 {
		return Always, nil
	}

	var fs []Filter
	for _, s := range ss {
		f, err := Parse(s)
		if err != nil {
			return nil, fmt.Errorf("invalid argument: %w", err)
		}

		fs = append(fs, f)
	}

	return Any(fs), nil
}

type parser struct {
	input   string
	scanner scanner
}

func (p *parser) parse() (Filter, error) {
	p.scanner.init(p.input)

	ss, err := p.selectors()
	if err != nil {
		return nil, fmt.Errorf("filters: %w", err)
	}

	return ss, nil
}

func (p *parser) selectors() (Filter, error) {
	s, err := p.selector()
	if err != nil {
		return nil, err
	}

	ss := All{s}

loop:
	for {
		tok := p.scanner.peek()
		switch tok {
		case ',':
			pos, tok, _ := p.scanner.scan()
			if tok != tokenSeparator {
				return nil, p.mkerr(pos, "expected a separator")
			}

			s, err := p.selector()
			if err != nil {
				return nil, err
			}

			ss = append(ss, s)
		case tokenEOF:
			break loop
		default:
			return nil, p.mkerr(p.scanner.ppos, "unexpected input: %v", string(tok))
		}
	}

	return ss, nil
}

func (p *parser) selector() (Filter, error) {
	var fieldpath []string
	if p.scanner.peek() != '*' {
		var err error
		fieldpath, err = p.fieldpath()
		if err != nil {
			return selector{}, err
		}
	}

	switch p.scanner.peek() {
	case ',', tokenSeparator, tokenEOF:
		return selector{
			fieldpath: fieldpath,
			operator:  operatorPresent,
		}, nil
	case '*':
		pos, tok, _ := p.scanner.scan() // consume '*'
		if tok != tokenWildcard {
			return nil, p.mkerr(pos, "expected a wildcard (`*`)")
		}

		switch p.scanner.peek() {
		case tokenEOF, ',':
			// Wildcard presence check (no operator/value)
			return wildcard{
				fieldpath: fieldpath,
				filter:    Always,
			}, nil
		case '.':
			pos, tok, _ := p.scanner.scan() // consume separator
			if tok != tokenSeparator {
				return nil, p.mkerr(pos, "expected a field separator (`.`)")
			}

			filter, err := p.selector()
			if err != nil {
				return nil, err
			}
			return wildcard{
				fieldpath: fieldpath,
				filter:    filter,
				negated:   isNegatedFilter(filter),
			}, nil
		}

		filter, err := p.selectorPart(nil)
		if err != nil {
			return nil, err
		}

		return wildcard{
			fieldpath: fieldpath,
			filter:    filter,
			negated:   isNegatedFilter(filter),
		}, nil
	}

	return p.selectorPart(fieldpath)
}

func (p *parser) selectorPart(fieldpath []string) (selector, error) {
	op, err := p.operator()
	if err != nil {
		return selector{}, err
	}

	var allowAltQuotes bool
	if op == operatorMatches || op == operatorNotMatches {
		allowAltQuotes = true
	}

	value, err := p.value(allowAltQuotes)
	if err != nil {
		if err == io.EOF {
			return selector{}, io.ErrUnexpectedEOF
		}
		return selector{}, err
	}

	sel := selector{
		fieldpath: fieldpath,
		value:     value,
		operator:  op,
	}
	if op == operatorMatches || op == operatorNotMatches {
		r, err := regexp.Compile(value)
		if err != nil {
			return selector{}, fmt.Errorf("failed to parse regular expression: %w", err)
		}
		sel.re = r
	}

	return sel, nil
}

func (p *parser) fieldpath() ([]string, error) {
	f, err := p.field()
	if err != nil {
		return nil, err
	}

	fs := []string{f}
loop:
	for {
		tok := p.scanner.peek() // lookahead to consume field separator

		switch tok {
		case '.':
			pos, tok, _ := p.scanner.scan() // consume separator
			if tok != tokenSeparator {
				return nil, p.mkerr(pos, "expected a field separator (`.`)")
			}

			if p.scanner.peek() == '*' {
				break loop
			}

			f, err := p.field()
			if err != nil {
				return nil, err
			}

			fs = append(fs, f)
		default:
			// let the layer above handle the other bad cases.
			break loop
		}
	}

	return fs, nil
}

func (p *parser) field() (string, error) {
	pos, tok, s := p.scanner.scan()
	switch tok {
	case tokenField:
		return s, nil
	case tokenQuoted:
		return p.unquote(pos, s, false)
	case tokenIllegal:
		return "", p.mkerr(pos, "%s", p.scanner.err)
	}

	return "", p.mkerr(pos, "expected field or quoted")
}

func (p *parser) operator() (operator, error) {
	pos, tok, s := p.scanner.scan()
	switch tok {
	case tokenOperator:
		switch s {
		case "=", "==":
			return operatorEqual, nil
		case "!=", "!==":
			return operatorNotEqual, nil
		case "~=":
			return operatorMatches, nil
		case "!~=":
			return operatorNotMatches, nil
		default:
			return 0, p.mkerr(pos, "unsupported operator %q", s)
		}
	case tokenIllegal:
		return 0, p.mkerr(pos, "%s", p.scanner.err)
	}

	return 0, p.mkerr(pos, `expected an operator ("="|"=="|"!="|"~=")`)
}

func (p *parser) value(allowAltQuotes bool) (string, error) {
	pos, tok, s := p.scanner.scan()

	switch tok {
	case tokenValue, tokenField:
		return s, nil
	case tokenQuoted:
		return p.unquote(pos, s, allowAltQuotes)
	case tokenIllegal:
		return "", p.mkerr(pos, "%s", p.scanner.err)
	}

	return "", p.mkerr(pos, "expected value or quoted")
}

func (p *parser) unquote(pos int, s string, allowAlts bool) (string, error) {
	if !allowAlts && s[0] != '\'' && s[0] != '"' {
		return "", p.mkerr(pos, "invalid quote encountered")
	}

	uq, err := unquote(s)
	if err != nil {
		return "", p.mkerr(pos, "unquoting failed: %v", err)
	}

	return uq, nil
}

type parseError struct {
	input string
	pos   int
	msg   string
}

func (pe parseError) Error() string {
	if pe.pos < len(pe.input) {
		before := pe.input[:pe.pos]
		location := pe.input[pe.pos : pe.pos+1] // need to handle end
		after := pe.input[pe.pos+1:]

		return fmt.Sprintf("[%s >|%s|< %s]: %v", before, location, after, pe.msg)
	}

	return fmt.Sprintf("[%s]: %v", pe.input, pe.msg)
}

func (p *parser) mkerr(pos int, format string, args ...any) error {
	return fmt.Errorf("parse error: %w", parseError{
		input: p.input,
		pos:   pos,
		msg:   fmt.Sprintf(format, args...),
	})
}
