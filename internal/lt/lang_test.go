package lt

import "testing"

func TestLanguageTable(t *testing.T) {
	// The bug this table exists for: a substring match ("GB"/"CA"/"AU"/"IN"
	// anywhere in the string, everything else American) made an unknown language
	// return 200 with zero matches — a document nobody checked, reported clean.
	if Supported("de-DE") {
		t.Error("de-DE must not be supported")
	}
	if Supported("EN-GB") || Supported("en-US ") {
		t.Error("codes are exact and lower-case: no case folding, no trimming")
	}

	gb, ok := Lookup("en-GB")
	if !ok || gb.Dialect != "British" || gb.Name != "English (UK)" {
		t.Errorf("Lookup(en-GB) = %+v, ok=%v", gb, ok)
	}
	// An omitted language keeps meaning American English, as it did before.
	if l, ok := Lookup(""); !ok || l.Code != "en-US" {
		t.Errorf(`Lookup("") = %+v, ok=%v; want the en-US default`, l, ok)
	}
	if l, _ := Lookup("en"); l.Code != "en-US" {
		t.Errorf("Lookup(en) = %+v; want en-US (LanguageTool's alias)", l)
	}

	for _, l := range Languages {
		if l.Code == "" || l.Name == "" || l.Dialect == "" {
			t.Errorf("incomplete entry: %+v", l)
		}
		if _, ok := byCode[l.Code]; !ok {
			t.Errorf("%q missing from byCode", l.Code)
		}
	}
	if got := len(SupportedCodes()); got != len(Languages) {
		t.Errorf("SupportedCodes() = %d codes, table has %d", got, len(Languages))
	}
	for _, c := range SupportedCodes() {
		if !Supported(c) {
			t.Errorf("SupportedCodes advertises %q but Supported() says no", c)
		}
	}
}
