// SPDX-License-Identifier: BSD-3-Clause
// Copyright (c) 2026, Unikraft GmbH and The Unikraft CLI Authors.
// Licensed under the BSD-3-Clause License (the "License").
// You may not use this file except in compliance with the License.

package script

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"unikraft.com/x/integration/tuikit"
)

type CommandKind int

const (
	CommandSleep CommandKind = iota
	CommandKey
	CommandWait
	CommandSnapshot
)

func (k CommandKind) String() string {
	switch k {
	case CommandSleep:
		return "sleep"
	case CommandKey:
		return "key"
	case CommandWait:
		return "wait"
	case CommandSnapshot:
		return "snapshot"
	default:
		return "unknown"
	}
}

type Command struct {
	Kind CommandKind

	Sleep time.Duration
	Key   tea.Key
	Wait  BoolExpr
}

// parseLine parses a single script line.
//
// Grammar:
//
//	sleep <time.Duration>
//	key <key>
//	wait <expr>
//	snapshot
//	# comment
func parseLine(raw string) (Command, bool, error) {
	toks, err := lexLine(raw)
	if err != nil {
		return Command{}, false, err
	}
	if len(toks) == 0 || toks[0].typ == tokEOF {
		return Command{}, true, nil
	}
	if toks[0].typ != tokWord {
		return Command{}, false, fmt.Errorf("expected command at %d", toks[0].pos)
	}

	cmd := strings.ToLower(toks[0].lit)
	switch cmd {
	case "snapshot":
		if len(toks) < 2 || toks[1].typ != tokEOF {
			return Command{}, false, fmt.Errorf("snapshot takes no arguments")
		}
		return Command{Kind: CommandSnapshot}, false, nil
	case "sleep":
		if len(toks) < 2 || toks[1].typ != tokWord {
			return Command{}, false, fmt.Errorf("sleep requires a duration")
		}
		if len(toks) < 3 || toks[2].typ != tokEOF {
			return Command{}, false, fmt.Errorf("sleep takes a single duration")
		}
		d, err := time.ParseDuration(toks[1].lit)
		if err != nil {
			return Command{}, false, err
		}
		return Command{Kind: CommandSleep, Sleep: d}, false, nil
	case "key":
		if len(toks) < 2 || (toks[1].typ != tokWord && toks[1].typ != tokString) {
			return Command{}, false, fmt.Errorf("key requires a key spec")
		}
		if len(toks) < 3 || toks[2].typ != tokEOF {
			return Command{}, false, fmt.Errorf("key takes a single key spec")
		}
		k, err := tuikit.ParseKey(toks[1].lit)
		if err != nil {
			return Command{}, false, err
		}
		return Command{Kind: CommandKey, Key: k}, false, nil
	case "wait":
		if len(toks) < 2 || toks[1].typ == tokEOF {
			return Command{}, false, fmt.Errorf("wait requires an expression")
		}
		expr, err := parseWaitExpr(toks[1:])
		if err != nil {
			return Command{}, false, err
		}
		return Command{Kind: CommandWait, Wait: expr}, false, nil
	default:
		return Command{}, false, fmt.Errorf("unknown command: %q", cmd)
	}
}

// ---- Wait expressions ----

type BoolExpr interface {
	Eval(snapshot string) bool
}

type ContainsExpr struct{ Needle string }

func (e *ContainsExpr) Eval(snapshot string) bool {
	return strings.Contains(snapshot, e.Needle)
}

type NotExpr struct{ X BoolExpr }

func (e *NotExpr) Eval(snapshot string) bool {
	return e.X != nil && !e.X.Eval(snapshot)
}

type AndExpr struct {
	Left  BoolExpr
	Right BoolExpr
}

func (e *AndExpr) Eval(snapshot string) bool {
	if e.Left == nil || e.Right == nil {
		return false
	}
	return e.Left.Eval(snapshot) && e.Right.Eval(snapshot)
}

type OrExpr struct {
	Left  BoolExpr
	Right BoolExpr
}

func (e *OrExpr) Eval(snapshot string) bool {
	if e.Left == nil || e.Right == nil {
		return false
	}
	return e.Left.Eval(snapshot) || e.Right.Eval(snapshot)
}

func parseWaitExpr(toks []token) (BoolExpr, error) {
	p := &exprParser{toks: toks}
	p.next()

	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.tok.typ != tokEOF {
		return nil, p.errorf("unexpected token %q", p.tok.lit)
	}
	return expr, nil
}

type exprParser struct {
	toks []token
	i    int
	tok  token
}

func (p *exprParser) next() {
	if p.i >= len(p.toks) {
		p.tok = token{typ: tokEOF, pos: -1}
		return
	}
	p.tok = p.toks[p.i]
	p.i++
}

func (p *exprParser) errorf(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	pos := max(0, p.tok.pos)
	return fmt.Errorf("%s at %d", msg, pos)
}

func (p *exprParser) acceptWord(want string) bool {
	if p.tok.typ == tokWord && strings.EqualFold(p.tok.lit, want) {
		p.next()
		return true
	}
	return false
}

func (p *exprParser) expect(tt tokenType, lit string) error {
	if p.tok.typ != tt {
		return p.errorf("expected %q", lit)
	}
	p.next()
	return nil
}

func (p *exprParser) parseOr() (BoolExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.acceptWord("or") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &OrExpr{Left: left, Right: right}
	}
	return left, nil
}

func (p *exprParser) parseAnd() (BoolExpr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.acceptWord("and") {
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &AndExpr{Left: left, Right: right}
	}
	return left, nil
}

func (p *exprParser) parseUnary() (BoolExpr, error) {
	if p.acceptWord("not") {
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &NotExpr{X: x}, nil
	}
	return p.parsePrimary()
}

func (p *exprParser) parsePrimary() (BoolExpr, error) {
	switch p.tok.typ {
	case tokLParen:
		p.next()
		expr, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
		return expr, nil
	case tokWord:
		if !strings.EqualFold(p.tok.lit, "contains") {
			return nil, p.errorf("unknown identifier %q", p.tok.lit)
		}
		p.next()
		if err := p.expect(tokLParen, "("); err != nil {
			return nil, err
		}
		if p.tok.typ != tokString {
			return nil, p.errorf("expected string")
		}
		needle := p.tok.lit
		p.next()
		if err := p.expect(tokRParen, ")"); err != nil {
			return nil, err
		}
		return &ContainsExpr{Needle: needle}, nil
	default:
		return nil, p.errorf("unexpected token %q", p.tok.lit)
	}
}
