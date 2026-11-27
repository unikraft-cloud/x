// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package script

import (
	"fmt"
	"strconv"
)

type tokenType int

const (
	tokEOF tokenType = iota
	tokWord
	tokString
	tokLParen
	tokRParen
)

type token struct {
	typ tokenType
	lit string
	pos int // byte offset in the input
}

func lexLine(src string) ([]token, error) {
	l := &lexer{src: src}
	var toks []token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.typ == tokEOF {
			return toks, nil
		}
	}
}

type lexer struct {
	src string
	off int
}

func (l *lexer) next() (token, error) {
	// Skip whitespace.
	for l.off < len(l.src) {
		switch l.src[l.off] {
		case ' ', '\t':
			l.off++
			continue
		case '#':
			// Comment start.
			l.off = len(l.src)
			return token{typ: tokEOF, pos: l.off}, nil
		}
		break
	}

	if l.off >= len(l.src) {
		return token{typ: tokEOF, pos: l.off}, nil
	}

	start := l.off
	switch l.src[l.off] {
	case '(':
		l.off++
		return token{typ: tokLParen, lit: "(", pos: start}, nil
	case ')':
		l.off++
		return token{typ: tokRParen, lit: ")", pos: start}, nil
	case '"':
		// Quoted string.
		l.off++
		esc := false
		for l.off < len(l.src) {
			c := l.src[l.off]
			l.off++
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				raw := l.src[start:l.off]
				unq, err := strconv.Unquote(raw)
				if err != nil {
					return token{}, err
				}
				return token{typ: tokString, lit: unq, pos: start}, nil
			}
		}
		return token{}, fmt.Errorf("unterminated string at %d", start)
	default:
		// Word token. Read until whitespace or a structural delimiter.
		for l.off < len(l.src) {
			switch l.src[l.off] {
			case ' ', '\t', '(', ')', '"':
				goto done
			default:
				l.off++
			}
		}
	}

done:
	if l.off == start {
		return token{}, fmt.Errorf("unexpected character %q at %d", l.src[l.off], l.off)
	}
	return token{typ: tokWord, lit: l.src[start:l.off], pos: start}, nil
}
