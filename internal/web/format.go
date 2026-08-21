package web

import (
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// DietLabel names a diet in the reader's language, falling back to the stored
// value so an unknown one shows up as itself rather than vanishing.
func DietLabel(lang i18n.Lang, d store.Diet) string {
	if d == "" {
		d = store.DietOmnivore
	}
	key := "diet." + string(d)
	if !i18n.Has(key) {
		return string(d)
	}
	return i18n.T(lang, key)
}

// DietHint explains a diet in a few words.
func DietHint(lang i18n.Lang, d store.Diet) string {
	key := "diet." + string(d) + ".hint"
	if !i18n.Has(key) {
		return ""
	}
	return i18n.T(lang, key)
}
