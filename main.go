package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
)

//go:embed static
var content embed.FS

func main() {
	port := os.Getenv("PORT") // Render sets PORT automatically
	if port == "" {
		port = "8080"
	}
	sub, _ := fs.Sub(content, "static")
	http.Handle("/", http.FileServer(http.FS(sub)))
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	log.Printf("Chess server running on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

