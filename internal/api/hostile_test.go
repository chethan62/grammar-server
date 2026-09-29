package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// A server that answers garbage with a 5xx is broken from the client's side, and one
// that hangs is worse. These swept every endpoint by hand once; they live here so the
// guarantee survives the next refactor.
//
// /v2/rewrite is deliberately absent: without a local Ollama it answers 503, which is
// a legitimate 5xx, and rewrite_test.go already covers its own guards.
var hostileBodies = []struct{ name, body string }{
	{"empty body", ""},
	{"not JSON", "nonsense"},
	{"JSON array", "[1,2,3]"},
	{"JSON null", "null"},
	{"JSON string", `"hello"`},
	{"JSON number", "42"},
	{"no text field", "{}"},
	{"text is a number", `{"text":5}`},
	{"text is null", `{"text":null}`},
	{"language is an array", `{"text":"hi","language":[]}`},
	{"rules is an object", `{"text":"hi","enabledRules":{}}`},
	{"rules contain nulls", `{"text":"hi","enabledRules":[null,""]}`},
	{"category named like a prototype", `{"text":"hi","enabledCategories":["__proto__","<script>"]}`},
	{"level is a number", `{"text":"hi","level":7}`},
	{"enabledOnly is a string", `{"text":"hi","enabledOnly":"yes"}`},
	{"nested arrays where rules go", `{"text":"hi","enabledRules":[[[["x"]]]]}`},
	{"text and data at once", `{"text":"hi","data":"aGk="}`},
	{"data is not base64", `{"data":"!!! not base64 !!!"}`},
	{"only NUL bytes", `{"text":"\u0000\u0000"}`},
	{"only newlines", `{"text":"\n\n\n"}`},
	{"only emoji", `{"text":"\ud83d\ude00\ud83d\ude00"}`},
	{"combining marks", `{"text":"e\u0301\u0301\u0301 teh"}`},
	{"right-to-left", `{"text":"\u05e9\u05dc\u05d5\u05dd teh"}`},
	{"duplicate keys", `{"text":"teh","text":"the"}`},
	{"absurd language code", `{"text":"hi","language":"` + strings.Repeat("e", 5000) + `"}`},
	{"long single word", `{"text":"` + strings.Repeat("x", 50_000) + `"}`},
}

func TestHostileInputNeverBreaksTheServer(t *testing.T) {
	srv := newTestServer(t)
	for _, endpoint := range []string{"/v2/check", "/v2/stats", "/v2/fix-sentence"} {
		for _, tc := range hostileBodies {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				resp, err := http.Post(srv.URL+endpoint, "application/json", strings.NewReader(tc.body))
				if err != nil {
					t.Fatalf("request failed: %v", err)
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				switch {
				case resp.StatusCode >= 500:
					t.Errorf("status %d for hostile input (body %q)", resp.StatusCode, tc.body[:min(len(tc.body), 80)])
				case resp.StatusCode >= 400 && len(body) == 0:
					t.Errorf("status %d with an empty body — a client learns nothing", resp.StatusCode)
				}
			})
		}
	}
}

// The engine is one serialized harper-ls for the whole server, so the real question is
// not whether one bad request is refused but whether bad requests leave the next good
// one unable to run.
func TestTheEngineSurvivesAHostileMix(t *testing.T) {
	srv := newTestServer(t)
	good := `{"text":"She go to the office.","language":"en-US"}`

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			endpoint := []string{"/v2/check", "/v2/stats", "/v2/fix-sentence"}[i%3]
			body := good
			if i%3 != 0 {
				body = hostileBodies[i%len(hostileBodies)].body
			}
			resp, err := http.Post(srv.URL+endpoint, "application/json", strings.NewReader(body))
			if err != nil {
				t.Errorf("%s: %v", endpoint, err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 500 {
				t.Errorf("%s answered %d under load", endpoint, resp.StatusCode)
			}
		}()
	}
	wg.Wait()

	// A clean check after the storm must still see the error in it.
	resp, err := http.Post(srv.URL+"/v2/check", "application/json", strings.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Matches []checkMatch `json:"matches"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range out.Matches {
		if m.Rule.ID == "HE_VERB_AGR" && m.Offset == 4 {
			found = true
		}
	}
	if !found {
		t.Errorf("after %d hostile requests the engine no longer reports HE_VERB_AGR(4): %+v", 24, out.Matches)
	}
}
