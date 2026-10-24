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
	llm       = flag.Bool("llm", false, "start the read-only LLM API on its own port")
	llmPort   = flag.Int("llm-port", 8081, "port for the LLM API")
	boardFile = flag.String("data", "board.json", "path to the board JSON file")
	mu        sync.Mutex
)

func main() {
	flag.Parse()

	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}

	app := http.NewServeMux()
	app.Handle("/", http.FileServer(http.FS(web)))
	app.HandleFunc("GET /api/board", getBoard)
	app.HandleFunc("PUT /api/board", putBoard)
	app.HandleFunc("GET /api/llm-port", getLLMPort)

	// The LLM gets its own port with only read endpoints, so it has no way to change the boards
	llmAPI := http.NewServeMux()
	llmAPI.HandleFunc("GET /api/boards", listBoards)
	llmAPI.HandleFunc("GET /api/boards/{id}", getBoardByID)

	host, access := "127.0.0.1", "this machine only"
	if *lan {
		host, access = "", "whole network"
	}

	log.Printf("data in %s", *boardFile)
	if *llm {
		go serve("LLM API", host, *llmPort, access, llmAPI)
	}
	serve("board", host, *port, access, app)
}

func serve(name, host string, port int, access string, handler http.Handler) {
	log.Printf("%s listening on port %d (%s)", name, port, access)
	log.Fatal(http.ListenAndServe(fmt.Sprintf("%s:%d", host, port), handler))
}

// 0 when the LLM API is off
func getLLMPort(w http.ResponseWriter, r *http.Request) {
	if !*llm {
		writeJSON(w, 0)
		return
	}
	writeJSON(w, *llmPort)
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

// Board data, as saved by the frontend

type Comment struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
}

type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Desc      string    `json:"desc"`
	Column    string    `json:"column"`
	DueDate   string    `json:"dueDate"`
	CreatedAt string    `json:"createdAt"`
	BlockedBy []string  `json:"blockedBy"`
	Comments  []Comment `json:"comments"`
	Blocked   bool      `json:"blocked"`
}

type Board struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Tasks []Task `json:"tasks"`
}

// Same order as COLUMNS in web/app.js
var columns = []string{"backlog", "todo", "inprogress", "review", "done"}

func readBoards() ([]Board, error) {
	mu.Lock()
	data, err := os.ReadFile(*boardFile)
	mu.Unlock()
	if os.IsNotExist(err) {
		return []Board{}, nil
	}
	if err != nil {
		return nil, err
	}

	var saved struct {
		Boards []Board `json:"boards"`
		Tasks  []Task  `json:"tasks"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}

	// A file from before multiple boards that the browser hasn't converted yet
	if saved.Boards == nil {
		saved.Boards = []Board{{ID: "board-main", Name: "My Board", Tasks: saved.Tasks}}
	}

	for _, b := range saved.Boards {
		done := map[string]bool{}
		for _, t := range b.Tasks {
			done[t.ID] = t.Column == "done"
		}
		// Blocked if any blocker still exists and isn't done, like isBlocked() in web/app.js
		for i, t := range b.Tasks {
			for _, id := range t.BlockedBy {
				if isDone, exists := done[id]; exists && !isDone {
					b.Tasks[i].Blocked = true
				}
			}
		}
	}
	return saved.Boards, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func listBoards(w http.ResponseWriter, r *http.Request) {
	boards, err := readBoards()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type summary struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Tickets map[string]int `json:"tickets"`
		Blocked int            `json:"blocked"`
	}
	summaries := []summary{}
	for _, b := range boards {
		s := summary{ID: b.ID, Name: b.Name, Tickets: map[string]int{}}
		for _, col := range columns {
			s.Tickets[col] = 0
		}
		for _, t := range b.Tasks {
			s.Tickets[t.Column]++
			if t.Blocked {
				s.Blocked++
			}
		}
		summaries = append(summaries, s)
	}
	writeJSON(w, summaries)
}

func getBoardByID(w http.ResponseWriter, r *http.Request) {
	boards, err := readBoards()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, b := range boards {
		if b.ID == r.PathValue("id") {
			writeJSON(w, struct {
				Board
				Columns []string `json:"columns"`
			}{b, columns})
			return
		}
	}
	http.Error(w, "board not found", http.StatusNotFound)
}
