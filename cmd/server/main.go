// Command server is a LanguageTool-compatible grammar checking HTTP server
// backed by harper-ls (offline, privacy-first).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"grammar-server/internal/api"
	"grammar-server/internal/engine"
)

func main() {
	port := flag.Int("port", 8875, "listen port")
	dialect := flag.String("dialect", "American", "default harper dialect (American|British|Canadian|Australian|Indian)")
	harperBin := flag.String("harper", "harper-ls", "path to the harper-ls binary")
	flag.Parse()

	h, err := engine.NewHarper(*harperBin, *dialect, nil)
	if err != nil {
		log.Fatalf("engine init: %v", err)
	}
	defer h.Close()

	srv := api.NewServer(h)
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("grammar-server listening on %s (harper-ls: %s)", addr, *harperBin)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
