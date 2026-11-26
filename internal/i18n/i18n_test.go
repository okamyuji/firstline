package i18n

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/okamyuji/firstline/internal/domain"
)

func TestKeySetsMatchAndNoEmptyStrings(t *testing.T) {
	for k, v := range jaStrings {
		if v == "" {
			t.Errorf("ja %q is empty", k)
		}
		if _, ok := enStrings[k]; !ok {
			t.Errorf("key %q missing in en", k)
		}
	}
	for k, v := range enStrings {
		if v == "" {
			t.Errorf("en %q is empty", k)
		}
		if _, ok := jaStrings[k]; !ok {
			t.Errorf("key %q missing in ja", k)
		}
	}
}

// Every constant in keys.go must have a string, so a new key cannot ship untranslated.
func TestEveryKeyConstantIsTranslated(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "keys.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			for _, v := range s.(*ast.ValueSpec).Values {
				key, err := strconv.Unquote(v.(*ast.BasicLit).Value)
				if err != nil {
					t.Fatal(err)
				}
				n++
				if _, ok := jaStrings[key]; !ok {
					t.Errorf("constant %q has no ja string", key)
				}
			}
		}
	}
	if n != len(jaStrings) {
		t.Errorf("%d constants but %d strings", n, len(jaStrings))
	}
}

func TestPlaceholdersMatchBetweenLanguages(t *testing.T) {
	// English may reorder arguments with %[n]s, so compare the verb letters as sets.
	verb := regexp.MustCompile(`%(?:\[\d+\])?([a-z])`)
	letters := func(s string) string {
		var l []string
		for _, m := range verb.FindAllStringSubmatch(s, -1) {
			l = append(l, m[1])
		}
		slices.Sort(l)
		return strings.Join(l, "")
	}
	for k, ja := range jaStrings {
		if j, e := letters(ja), letters(enStrings[k]); j != e {
			t.Errorf("%q: ja verbs %v, en verbs %v", k, j, e)
		}
	}
}

func TestTranslate(t *testing.T) {
	tr := New()
	tests := []struct {
		name, lang, key string
		args            []any
		want            string
	}{
		{"ja", "ja", ErrSoldOut, nil, jaStrings[ErrSoldOut]},
		{"en", "en", ErrSoldOut, nil, enStrings[ErrSoldOut]},
		{"unknown language falls back to ja", "fr", ErrSoldOut, nil, jaStrings[ErrSoldOut]},
		{"empty language falls back to ja", "", ErrSoldOut, nil, jaStrings[ErrSoldOut]},
		{"unknown key returns the key", "en", "no.such.key", nil, "no.such.key"},
		{"args are formatted", "en", MailPurchasedSubject, []any{"Gig"}, fmt.Sprintf(enStrings[MailPurchasedSubject], "Gig")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tr.T(tt.lang, tt.key, tt.args...); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
	if got := tr.T("en", MailPurchasedSubject, "Gig"); got == enStrings[MailPurchasedSubject] {
		t.Error("placeholder was not replaced")
	}
}

func TestNegotiate(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "ja"},
		{"*", "ja"},
		{"en;q=0", "ja"},
		{"%%%", "ja"},
		{"fr", "ja"},
		{"en-US", "en"},
		{"en-GB,ja;q=0.5", "en"},
		{"ja,en", "ja"},
		{"en,ja", "en"},
		{"ja-JP", "ja"},
		{"fr,en;q=0.8", "en"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Negotiate(tt.in); got != tt.want {
				t.Errorf("Negotiate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestErrorKey(t *testing.T) {
	want := map[error]string{
		domain.ErrSoldOut:            ErrSoldOut,
		domain.ErrInvalidTransition:  ErrInvalidTransition,
		domain.ErrNotOnSale:          ErrNotOnSale,
		domain.ErrBadQuantity:        ErrBadQuantity,
		domain.ErrDuplicateHold:      ErrDuplicateHold,
		domain.ErrNotFound:           ErrNotFound,
		domain.ErrPaymentUnavailable: ErrPaymentUnavailable,
	}
	seen := map[string]bool{}
	for err, key := range want {
		if got := ErrorKey(fmt.Errorf("wrapped: %w", err)); got != key {
			t.Errorf("ErrorKey(%v) = %q, want %q", err, got, key)
		}
		seen[key] = true
	}
	if len(seen) != len(want) {
		t.Error("domain errors share keys")
	}
	if got := ErrorKey(errors.New("boom")); got != ErrInternal {
		t.Errorf("generic error key = %q, want %q", got, ErrInternal)
	}
	if seen[ErrInternal] {
		t.Error("generic key must differ from the specific keys")
	}
}
