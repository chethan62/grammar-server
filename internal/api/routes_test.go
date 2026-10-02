package api

import (
	"strings"
	"testing"
)

// /status advertises a hand-written list of endpoints next to the routes the mux serves, and a hand-written
// list beside the thing it describes is what drifts. It already had: /v2/pause was registered and reachable
// while missing from the advertisement, and nothing noticed, because nothing connected the two.
//
// This is the connection, and both directions matter. A route with no line is an endpoint nobody can
// discover; a line with no route is worse, because it tells a client to call something that 404s.
//
// No harper-ls needed: the routes are method values and the descriptions are a package var, so this runs on
// any machine rather than skipping with the engine tests.
func TestStatusAdvertisesEveryRoute(t *testing.T) {
	// A zero Server is enough here — routes() only takes method values off it.
	registered := make(map[string]bool)
	for pattern := range (&Server{}).routes() {
		registered[pattern] = true
	}
	if len(registered) == 0 {
		t.Fatal("no routes registered at all, so this test is not comparing anything")
	}

	advertised := make(map[string]bool)
	for _, doc := range endpointDocs {
		fields := strings.Fields(doc)
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "/") {
			t.Errorf("%q does not read as a method and a path, which is how this test finds the path", doc)
			continue
		}
		if fields[1] == "/" {
			// The index is what serves this list; advertising it as an endpoint would point a client at
			// itself.
			t.Errorf("%q advertises the index itself as an endpoint", doc)
		}
		advertised[fields[1]] = true
	}

	for pattern := range registered {
		if !advertised[pattern] {
			t.Errorf("%s is served but not advertised in /status, so a client reading the index cannot find it", pattern)
		}
	}
	for path := range advertised {
		if !registered[path] {
			t.Errorf("%s is advertised in /status but not served — a client that trusts the index gets a 404", path)
		}
	}
}
