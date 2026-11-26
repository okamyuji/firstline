// Package i18n holds every string a user reads, in Japanese and English.
package i18n

import (
	"errors"
	"fmt"

	"golang.org/x/text/language"

	"github.com/okamyuji/firstline/internal/domain"
)

type Translator interface {
	T(lang, key string, args ...any) string
}

type translator struct{}

func New() Translator { return translator{} }

// T falls back to Japanese for an unknown language and returns the key itself
// for an unknown key; the key-set tests make the latter unreachable.
func (translator) T(lang, key string, args ...any) string {
	m := jaStrings
	if lang == "en" {
		m = enStrings
	}
	s, ok := m[key]
	if !ok {
		return key
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Japanese comes first: the matcher returns the first supported tag when nothing matches.
var matcher = language.NewMatcher([]language.Tag{language.Japanese, language.English})

// Negotiate returns "ja" or "en". Any parse error or non-match gives "ja".
func Negotiate(acceptLanguage string) string {
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(tags) == 0 {
		return "ja"
	}
	_, idx, conf := matcher.Match(tags...)
	if conf == language.No || idx != 1 {
		return "ja"
	}
	return "en"
}

var errorKeys = []struct {
	err error
	key string
}{
	{domain.ErrSoldOut, ErrSoldOut},
	{domain.ErrInvalidTransition, ErrInvalidTransition},
	{domain.ErrNotOnSale, ErrNotOnSale},
	{domain.ErrBadQuantity, ErrBadQuantity},
	{domain.ErrDuplicateHold, ErrDuplicateHold},
	{domain.ErrNotFound, ErrNotFound},
	{domain.ErrPaymentUnavailable, ErrPaymentUnavailable},
}

// ErrorKey maps a domain error to its message key; other errors get the generic key.
func ErrorKey(err error) string {
	for _, e := range errorKeys {
		if errors.Is(err, e.err) {
			return e.key
		}
	}
	return ErrInternal
}
