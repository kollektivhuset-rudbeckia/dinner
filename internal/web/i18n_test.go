package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// A phrase the templates ask for but the catalogue does not have renders as
// ⟦key⟧ on the page. That is loud, but it should never get as far as a person:
// these tests read the templates and the catalogue and fail the build instead.

var (
	// {{t "some.key"}} and {{t "some.key" arg}}, including inside an argument
	// list such as (t "x") or dict "Zero" (t "y").
	tCall = regexp.MustCompile(`\bt\s+"([a-z0-9._]+)"`)
	// count "adult" 3 / plural "person" 1 — the unit names catalogue rows too.
	unitCall = regexp.MustCompile(`\b(?:count|plural)\s+"([a-z0-9._]+)"`)
)

func templateSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no templates found")
	}
	return out
}

func TestEveryPhraseTheTemplatesAskForExists(t *testing.T) {
	var missing []string
	for path, src := range templateSources(t) {
		for _, m := range tCall.FindAllStringSubmatch(src, -1) {
			if !i18n.Has(m[1]) {
				missing = append(missing, path+": "+m[1])
			}
		}
		for _, m := range unitCall.FindAllStringSubmatch(src, -1) {
			for _, suffix := range []string{".one", ".many"} {
				key := "unit." + m[1] + suffix
				if !i18n.Has(key) {
					missing = append(missing, path+": "+key)
				}
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("phrase is missing from the catalogue — %s", m)
	}
}

// Every diet needs a name and an explanation, or the form offers a blank row.
func TestEveryDietIsNamedAndExplained(t *testing.T) {
	for _, d := range store.Diets {
		for _, key := range []string{"diet." + string(d), "diet." + string(d) + ".hint"} {
			if !i18n.Has(key) {
				t.Errorf("%s is missing from the catalogue", key)
			}
		}
		for _, lang := range i18n.Langs {
			if label := DietLabel(lang, d); label == "" || label == string(d) {
				t.Errorf("%s has no %s label", d, lang)
			}
			if DietHint(lang, d) == "" {
				t.Errorf("%s has no %s explanation", d, lang)
			}
		}
	}
}

// Both columns of the catalogue have to be filled in. A blank translation
// would render as nothing at all, which is worse than the wrong language.
func TestEveryPhraseHasBothLanguages(t *testing.T) {
	for _, key := range i18n.Keys() {
		sv, en, ok := i18n.Entry(key)
		if !ok {
			t.Fatalf("%s vanished between Keys and Entry", key)
		}
		if strings.TrimSpace(sv) == "" {
			t.Errorf("%s has no Swedish", key)
		}
		if strings.TrimSpace(en) == "" {
			t.Errorf("%s has no English", key)
		}
	}
}

// A phrase with a %s in one language and none in the other would render with a
// stray "%!s(MISSING)" or silently drop its argument.
func TestPlaceholdersMatchBetweenLanguages(t *testing.T) {
	verbs := regexp.MustCompile(`%[a-zA-Z]`)
	for _, key := range i18n.Keys() {
		sv, en, _ := i18n.Entry(key)
		gotSV, gotEN := verbs.FindAllString(sv, -1), verbs.FindAllString(en, -1)
		if len(gotSV) != len(gotEN) {
			t.Errorf("%s takes %v in Swedish but %v in English", key, gotSV, gotEN)
			continue
		}
		// The order matters too: Sprintf fills them positionally.
		for i := range gotSV {
			if gotSV[i] != gotEN[i] {
				t.Errorf("%s: placeholder %d is %s in Swedish and %s in English",
					key, i+1, gotSV[i], gotEN[i])
			}
		}
	}
}

// Nothing in the templates should still be a Swedish sentence sitting outside
// the catalogue. This looks for the letters only Swedish uses, which is a crude
// but effective net for prose that was never keyed.
func TestNoUntranslatedSwedishLeftInTheTemplates(t *testing.T) {
	for path, src := range templateSources(t) {
		for i, line := range strings.Split(src, "\n") {
			// Comments explain the template to whoever maintains it and are
			// never rendered, so they are allowed to say anything.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "{{/*") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			// A catalogue lookup naturally contains the key, not prose.
			stripped := tCall.ReplaceAllString(line, "")
			if strings.ContainsAny(stripped, "åäöÅÄÖ") {
				t.Errorf("%s:%d looks like untranslated Swedish: %s", path, i+1, trimmed)
			}
		}
	}
}

// ---------------------------------------------------------- choosing one ---

func TestTheSwitchRemembersTheLanguageAndComesBack(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna@example.se")

	// Swedish to start with, since that is what the configuration says.
	if body := c.get("/").Body.String(); !strings.Contains(body, "Middagar i huset") {
		t.Fatal("the site should start out in Swedish")
	}

	rec := c.post("/sprak", url.Values{"lang": {"en"}, "next": {"/mina"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("switching = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/mina" {
		t.Errorf("came back to %q, want /mina", got)
	}
	if c.cookies[i18n.Cookie] != "en" {
		t.Errorf("cookie = %q", c.cookies[i18n.Cookie])
	}

	body := c.get("/").Body.String()
	if !strings.Contains(body, "Dinners in the house") {
		t.Error("the page should now be in English")
	}
	if strings.Contains(body, "Middagar i huset") {
		t.Error("the page is still partly Swedish")
	}
	// And the switch now offers the way back.
	if !strings.Contains(body, `value="sv"`) {
		t.Error("the switch should offer Swedish again")
	}

	// Back again.
	c.post("/sprak", url.Values{"lang": {"sv"}, "next": {"/"}})
	if body := c.get("/").Body.String(); !strings.Contains(body, "Middagar i huset") {
		t.Error("switching back should give Swedish")
	}
}

func TestTheSwitchRefusesALanguageWeDoNotHave(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	rec := c.post("/sprak", url.Values{"lang": {"de"}, "next": {"/"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("= %d, want 400", rec.Code)
	}
	if _, ok := c.cookies[i18n.Cookie]; ok {
		t.Error("no cookie should have been set")
	}
}

// A redirect target out of a form is not to be trusted here either.
func TestTheSwitchOnlyComesBackToThisSite(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	rec := c.post("/sprak", url.Values{"lang": {"en"}, "next": {"https://evil.example.com"}})
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

// Before anybody chooses, the browser's own preference is a better guess than
// the default.
func TestAcceptLanguageIsUsedUntilSomebodyChooses(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest("GET", "/login", nil)
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "The house password") {
		t.Error("an English browser should get the English login page")
	}

	// A language we do not have falls through to the deployment's own.
	req = httptest.NewRequest("GET", "/login", nil)
	req.Header.Set("Accept-Language", "de-DE,de;q=0.9")
	rec = httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Husets lösenord") {
		t.Error("a German browser should get the configured default, Swedish")
	}

	// An explicit choice beats the browser.
	req = httptest.NewRequest("GET", "/login", nil)
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	req.AddCookie(&http.Cookie{Name: i18n.Cookie, Value: "sv"})
	rec = httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Husets lösenord") {
		t.Error("the cookie should win over Accept-Language")
	}
}

// The whole page has to switch, dates included — a Swedish weekday inside an
// English sentence is the tell-tale of a half-done translation.
func TestDatesFollowTheLanguage(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna@example.se")

	sv := c.get("/middag/" + openDay).Body.String()
	// Swedish weekdays are lower case, so the page title-cases them where a
	// sentence starts — hence "Tisdag" here and "tisdag" mid-sentence.
	if !strings.Contains(sv, "Tisdag 25 augusti") {
		t.Error("the Swedish page should say Tisdag 25 augusti")
	}
	if !strings.Contains(sv, "fredag 21 augusti") {
		t.Error("the Swedish page should give the deadline in Swedish")
	}

	c.post("/sprak", url.Values{"lang": {"en"}, "next": {"/"}})
	en := c.get("/middag/" + openDay).Body.String()
	if !strings.Contains(en, "Tuesday 25 August") {
		t.Error("the English page should say Tuesday 25 August")
	}
	for _, swedish := range []string{"augusti", "tisdag", "fredag"} {
		if strings.Contains(en, swedish) {
			t.Errorf("the English page still contains %q", swedish)
		}
	}
}

// The list is what the cooking team reads, so it has to switch too.
func TestTheListAndItsDietsFollowTheLanguage(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna@example.se")
	c.post("/middag/"+openDay, party(2, 1, store.DietFlexitarian, "no shellfish"))

	c.post("/sprak", url.Values{"lang": {"en"}, "next": {"/"}})
	body := c.get("/middag/" + openDay + "/lista").Body.String()
	for _, want := range []string{"Dinner list", "Who is coming", "Flexitarian", "Portions:"} {
		if !strings.Contains(body, want) {
			t.Errorf("the English list is missing %q", want)
		}
	}
}

// The cooking-team leader's mail cannot follow a browser cookie, because it is
// not sent to a browser. It follows the deployment's own language.
func TestTheMailUsesTheConfiguredLanguage(t *testing.T) {
	h := newHarness(t)
	if got := h.defaultLang(); got != i18n.SV {
		t.Fatalf("the test harness should be Swedish, got %q", got)
	}
	// A reader switching their own browser to English must not change it.
	c := h.client(t)
	c.post("/sprak", url.Values{"lang": {"en"}, "next": {"/"}})
	if got := h.defaultLang(); got != i18n.SV {
		t.Errorf("defaultLang = %q after a reader switched language", got)
	}
	if got := i18n.T(h.defaultLang(), "mail.open"); got != "Öppna matlistan" {
		t.Errorf("the mail would say %q", got)
	}
}
