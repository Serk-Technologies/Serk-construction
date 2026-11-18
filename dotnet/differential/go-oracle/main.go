// Command go-oracle runs the Go reference implementation on differential test
// cases and prints the canonical results (ADR 0026: the Go repository is the
// behavioural reference for the .NET migration).
//
// usage: go run ./dotnet/differential/go-oracle CASES.json > RESULTS.json
//
// Each case is {"id", "op", ...arguments}; each result is {"id", "ok", "value"}
// or {"id", "ok": false, "error": <class>}. Error classes are coarse on
// purpose (invalid_amount, unknown_currency, ...): the harness compares what
// is refused, not the wording.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/ethcal"
	"github.com/serk-group/serk-construction/pkg/money"
)

type kase struct {
	ID       string  `json:"id"`
	Op       string  `json:"op"`
	Amount   string  `json:"amount,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Policy   string  `json:"policy,omitempty"`
	Places   int32   `json:"places,omitempty"`
	Factor   string  `json:"factor,omitempty"`
	Ratios   []int64 `json:"ratios,omitempty"`
	JDN      int     `json:"jdn,omitempty"`
	Text     string  `json:"text,omitempty"`
	Y        int     `json:"y,omitempty"`
	M        int     `json:"m,omitempty"`
	D        int     `json:"d,omitempty"`
}

type result struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Value any    `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

func class(err error) string {
	switch {
	case errors.Is(err, money.ErrInvalidAmount):
		return "invalid_amount"
	case errors.Is(err, money.ErrUnknownCurrency):
		return "unknown_currency"
	case errors.Is(err, money.ErrUnknownRounding):
		return "unknown_rounding"
	case errors.Is(err, ethcal.ErrInvalid):
		return "invalid_date"
	case strings.Contains(err.Error(), "division by zero"):
		return "division_by_zero"
	case strings.Contains(err.Error(), "ratio"):
		return "invalid_ratios"
	}
	return "other:" + err.Error()
}

func run(c kase) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	switch c.Op {
	case "money.parse_strict":
		m, err := money.ParseStrict(c.Amount, money.Currency(c.Currency))
		if err != nil {
			return nil, err
		}
		return m.AmountString() + " " + string(m.Currency()), nil
	case "money.round":
		d, err := decimal.NewFromString(c.Amount)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", money.ErrInvalidAmount, err)
		}
		r, err := money.ApplyRounding(d, c.Places, money.RoundingPolicy(c.Policy))
		if err != nil {
			return nil, err
		}
		return r.StringFixed(c.Places), nil
	case "money.mul", "money.div":
		m, err := money.ParseStrict(c.Amount, money.Currency(c.Currency))
		if err != nil {
			return nil, err
		}
		f, err := decimal.NewFromString(c.Factor)
		if err != nil {
			return nil, fmt.Errorf("%w: factor %v", money.ErrInvalidAmount, err)
		}
		var out money.Money
		if c.Op == "money.mul" {
			out, err = m.Mul(f, money.RoundingPolicy(c.Policy))
		} else {
			out, err = m.Div(f, money.RoundingPolicy(c.Policy))
		}
		if err != nil {
			return nil, err
		}
		return out.AmountString(), nil
	case "money.allocate":
		m, err := money.ParseStrict(c.Amount, money.Currency(c.Currency))
		if err != nil {
			return nil, err
		}
		parts, err := m.Allocate(c.Ratios)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(parts))
		for i, p := range parts {
			out[i] = p.AmountString()
		}
		return out, nil
	case "eth.from_jdn":
		d := ethcal.FromJDN(c.JDN)
		return d.ISO(), nil
	case "eth.from_gregorian":
		t, err := time.Parse("2006-01-02", c.Text)
		if err != nil {
			return nil, err
		}
		return ethcal.FromGregorian(t).ISO(), nil
	case "eth.to_gregorian":
		t, err := ethcal.Date{Year: c.Y, Month: c.M, Day: c.D}.Gregorian()
		if err != nil {
			return nil, err
		}
		return t.Format("2006-01-02"), nil
	case "eth.parse":
		d, err := ethcal.Parse(c.Text)
		if err != nil {
			return nil, err
		}
		return d.ISO() + " " + d.String(), nil
	}
	return nil, fmt.Errorf("unknown op %q", c.Op)
}

// isolated runs one case in a child process killed after a deadline, so a Go
// function that never returns (pkg/money Allocate after a 64-bit overflow, see
// DIFF-002) is recorded as "nontermination" instead of hanging the harness.
func isolated(c kase) result {
	b, _ := json.Marshal(c)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, os.Args[0], "-one", string(b)).Output()
	if ctx.Err() != nil {
		return result{ID: c.ID, Error: "nontermination"}
	}
	if err != nil {
		return result{ID: c.ID, Error: "other:child " + err.Error()}
	}
	var r result
	if err := json.Unmarshal(out, &r); err != nil {
		return result{ID: c.ID, Error: "other:child output"}
	}
	return r
}

func one(c kase) result {
	v, err := run(c)
	if err != nil {
		return result{ID: c.ID, Error: class(err)}
	}
	return result{ID: c.ID, OK: true, Value: v}
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "-one" {
		var c kase
		if err := json.Unmarshal([]byte(os.Args[2]), &c); err != nil {
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stdout).Encode(one(c))
		return
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go-oracle CASES.json")
		os.Exit(2)
	}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var cases []kase
	if err := json.Unmarshal(b, &cases); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := make([]result, 0, len(cases))
	for _, c := range cases {
		if c.Op == "money.allocate" {
			out = append(out, isolated(c))
			continue
		}
		out = append(out, one(c))
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(out); err != nil {
		os.Exit(1)
	}
}
