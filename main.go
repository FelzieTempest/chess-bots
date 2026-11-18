package main

import (
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
)

//go:embed static
var content embed.FS

func main() {
	sub, _ := fs.Sub(content, "static")
	http.Handle("/", http.FileServer(http.FS(sub)))
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	port := os.Getenv("PORT") // Render sets PORT automatically
	if port != "" {
		log.Printf("Chess server running on :%s", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}

	// Desktop mode: pick a free local port and open the game in its own app window
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	go openWindow(url)
	log.Fatal(http.Serve(ln, nil))
}

func openWindow(url string) {
	if runtime.GOOS == "windows" {
		// Edge app mode = standalone window without browser UI
		if exec.Command("cmd", "/c", "start", "", "msedge", "--app="+url, "--window-size=1000,780").Run() == nil {
			return
		}
		exec.Command("cmd", "/c", "start", "", url).Run()
		return
	}
	exec.Command("xdg-open", url).Run()
}
