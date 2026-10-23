package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"
)

//go:embed web
var webFiles embed.FS

var (
	lan       = flag.Bool("lan", false, "allow other devices on the network to connect")
	port      = flag.Int("port", 8080, "port to listen on")
	boardFile = flag.String("data", "board.json", "path to the board JSON file")
	mu        sync.Mutex
)

func main() {
	flag.Parse()

	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/", http.FileServer(http.FS(web)))
	http.HandleFunc("GET /api/board", getBoard)
	http.HandleFunc("PUT /api/board", putBoard)

	host, access := "127.0.0.1", "this machine only"
	if *lan {
		host, access = "", "whole network"
	}
	addr := fmt.Sprintf("%s:%d", host, *port)

	log.Printf("listening on port %d (%s), data in %s", *port, access, *boardFile)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func getBoard(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	data, err := os.ReadFile(*boardFile)
	mu.Unlock()

	// No board yet: the frontend creates one on first load
	if os.IsNotExist(err) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func putBoard(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !json.Valid(data) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	mu.Lock()
	defer mu.Unlock()

	// Write to a temp file and rename, so a crash mid-write can't corrupt the board
	tmp := *boardFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, *boardFile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
