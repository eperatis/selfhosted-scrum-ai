package main

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var embeddedWebFiles embed.FS

var (
	allowNetworkAccess = flag.Bool("lan", false, "allow other devices on the network to connect")
	boardPort          = flag.Int("port", 8080, "port to listen on")
	enableLLMAPI       = flag.Bool("llm", false, "start the LLM API on its own port")
	llmAPIPort         = flag.Int("llm-port", 8081, "port for the LLM API")
	boardFilePath      = flag.String("data", "board.json", "path to the board JSON file")
	boardFileMutex     sync.Mutex
)

const (
	maxBoardFileUploadBytes = 10 << 20
	maxLLMRequestBodyBytes  = 1 << 20
)

func main() {
	flag.Parse()

	webFiles, err := fs.Sub(embeddedWebFiles, "web")
	if err != nil {
		log.Fatal(err)
	}

	boardRouter := http.NewServeMux()
	boardRouter.Handle("/", http.FileServer(http.FS(webFiles)))
	boardRouter.HandleFunc("GET /api/board", handleGetBoardFile)
	boardRouter.HandleFunc("PUT /api/board", handlePutBoardFile)
	boardRouter.HandleFunc("GET /api/llm-port", handleGetLLMAPIPort)

	// The LLM gets its own port where it can read and create, but has no way to change or delete anything
	llmAPIRouter := http.NewServeMux()
	llmAPIRouter.HandleFunc("GET /api/boards", handleListBoardSummaries)
	llmAPIRouter.HandleFunc("GET /api/boards/{id}", handleGetBoardByID)
	llmAPIRouter.HandleFunc("POST /api/boards", handleCreateBoard)
	llmAPIRouter.HandleFunc("POST /api/boards/{id}/tickets", handleCreateTicket)

	listenHost, accessDescription := "127.0.0.1", "this machine only"
	if *allowNetworkAccess {
		listenHost, accessDescription = "", "whole network"
	}

	log.Printf("data in %s", *boardFilePath)
	if *enableLLMAPI {
		go listenAndServe("LLM API", listenHost, *llmAPIPort, accessDescription, llmAPIRouter)
	}
	listenAndServe("board", listenHost, *boardPort, accessDescription, boardRouter)
}

func listenAndServe(serverName, listenHost string, listenPort int, accessDescription string, handler http.Handler) {
	log.Printf("%s listening on port %d (%s)", serverName, listenPort, accessDescription)
	log.Fatal(http.ListenAndServe(fmt.Sprintf("%s:%d", listenHost, listenPort), handler))
}

// 0 when the LLM API is off
func handleGetLLMAPIPort(responseWriter http.ResponseWriter, request *http.Request) {
	if !*enableLLMAPI {
		writeJSONResponse(responseWriter, http.StatusOK, 0)
		return
	}
	writeJSONResponse(responseWriter, http.StatusOK, *llmAPIPort)
}

func handleGetBoardFile(responseWriter http.ResponseWriter, request *http.Request) {
	boardFileMutex.Lock()
	boardFileContents, err := os.ReadFile(*boardFilePath)
	boardFileMutex.Unlock()

	// No board yet: the frontend creates one on first load
	if os.IsNotExist(err) {
		responseWriter.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
		return
	}

	responseWriter.Header().Set("ETag", computeETag(boardFileContents))
	responseWriter.Header().Set("Content-Type", "application/json")
	if _, err := responseWriter.Write(boardFileContents); err != nil {
		log.Printf("writing board file response failed: %v", err)
	}
}

func handlePutBoardFile(responseWriter http.ResponseWriter, request *http.Request) {
	uploadedBoardFile, err := io.ReadAll(http.MaxBytesReader(responseWriter, request.Body, maxBoardFileUploadBytes))
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
		return
	}
	if !json.Valid(uploadedBoardFile) {
		http.Error(responseWriter, "invalid JSON", http.StatusBadRequest)
		return
	}

	boardFileMutex.Lock()
	defer boardFileMutex.Unlock()

	// The browser sends the ETag it loaded; a mismatch means the LLM API changed the file since,
	// and saving would wipe out what it added
	if expectedETag := request.Header.Get("If-Match"); expectedETag != "" {
		currentBoardFile, err := os.ReadFile(*boardFilePath)
		if err != nil && !os.IsNotExist(err) {
			http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
			return
		}
		if expectedETag != computeETag(currentBoardFile) {
			http.Error(responseWriter, "board changed since it was loaded", http.StatusPreconditionFailed)
			return
		}
	}

	if err := writeBoardFileAtomically(uploadedBoardFile); err != nil {
		http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
		return
	}

	responseWriter.Header().Set("ETag", computeETag(uploadedBoardFile))
	responseWriter.WriteHeader(http.StatusNoContent)
}

func computeETag(boardFileContents []byte) string {
	return fmt.Sprintf(`"%x"`, sha256.Sum256(boardFileContents))
}

// Write to a temp file and rename, so a crash mid-write can't corrupt the board. Call with boardFileMutex held.
func writeBoardFileAtomically(boardFileContents []byte) error {
	temporaryFilePath := *boardFilePath + ".tmp"
	if err := os.WriteFile(temporaryFilePath, boardFileContents, 0o644); err != nil {
		return err
	}
	return os.Rename(temporaryFilePath, *boardFilePath)
}

// Board data, as saved by the frontend

type Comment struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
}

type Task struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"desc"`
	Column      string    `json:"column"`
	DueDate     string    `json:"dueDate"`
	CreatedAt   string    `json:"createdAt"`
	BlockedBy   []string  `json:"blockedBy"`
	Comments    []Comment `json:"comments"`
}

type Board struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Tasks []Task `json:"tasks"`
}

type BoardFile struct {
	Boards []Board `json:"boards"`
}

// Same order as COLUMNS in web/app.js
var columnIDs = []string{"backlog", "todo", "inprogress", "review", "done"}

// Blocked if any blocker still exists and isn't done, like isTaskBlocked() in web/app.js
func isTaskBlocked(board Board, task Task) bool {
	return slices.ContainsFunc(board.Tasks, func(otherTask Task) bool {
		return slices.Contains(task.BlockedBy, otherTask.ID) && otherTask.Column != "done"
	})
}

func readBoardsFromFile() ([]Board, error) {
	boardFileMutex.Lock()
	defer boardFileMutex.Unlock()
	return loadBoardsFromFile()
}

// Call with boardFileMutex held
func loadBoardsFromFile() ([]Board, error) {
	boardFileContents, err := os.ReadFile(*boardFilePath)
	if os.IsNotExist(err) {
		return []Board{}, nil
	}
	if err != nil {
		return nil, err
	}

	var boardFile BoardFile
	if err := json.Unmarshal(boardFileContents, &boardFile); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", *boardFilePath, err)
	}
	if boardFile.Boards == nil {
		return nil, fmt.Errorf("%s has no \"boards\" list", *boardFilePath)
	}
	return boardFile.Boards, nil
}

// Loads the boards, lets applyChange edit them and saves the result, all under one lock
// so a browser save can't land in between
func updateBoardsInFile(applyChange func([]Board) ([]Board, error)) error {
	boardFileMutex.Lock()
	defer boardFileMutex.Unlock()

	boards, err := loadBoardsFromFile()
	if err != nil {
		return err
	}
	boards, err = applyChange(boards)
	if err != nil {
		return err
	}
	boardFileContents, err := json.MarshalIndent(BoardFile{Boards: boards}, "", "  ")
	if err != nil {
		return err
	}
	return writeBoardFileAtomically(boardFileContents)
}

func writeJSONResponse(responseWriter http.ResponseWriter, statusCode int, responseBody any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	encoder := json.NewEncoder(responseWriter)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(responseBody); err != nil {
		log.Printf("writing JSON response failed: %v", err)
	}
}

func handleListBoardSummaries(responseWriter http.ResponseWriter, request *http.Request) {
	boards, err := readBoardsFromFile()
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
		return
	}

	type boardSummary struct {
		ID                   string         `json:"id"`
		Name                 string         `json:"name"`
		TicketCountPerColumn map[string]int `json:"tickets"`
		BlockedTicketCount   int            `json:"blocked"`
	}
	boardSummaries := []boardSummary{}
	for _, board := range boards {
		summary := boardSummary{ID: board.ID, Name: board.Name, TicketCountPerColumn: map[string]int{}}
		for _, columnID := range columnIDs {
			summary.TicketCountPerColumn[columnID] = 0
		}
		for _, task := range board.Tasks {
			summary.TicketCountPerColumn[task.Column]++
			if isTaskBlocked(board, task) {
				summary.BlockedTicketCount++
			}
		}
		boardSummaries = append(boardSummaries, summary)
	}
	writeJSONResponse(responseWriter, http.StatusOK, boardSummaries)
}

func handleGetBoardByID(responseWriter http.ResponseWriter, request *http.Request) {
	boards, err := readBoardsFromFile()
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
		return
	}

	boardIndex := slices.IndexFunc(boards, func(board Board) bool { return board.ID == request.PathValue("id") })
	if boardIndex < 0 {
		http.Error(responseWriter, "board not found", http.StatusNotFound)
		return
	}
	board := boards[boardIndex]

	type taskWithBlockedFlag struct {
		Task
		Blocked bool `json:"blocked"`
	}
	tasksWithBlockedFlag := []taskWithBlockedFlag{}
	for _, task := range board.Tasks {
		tasksWithBlockedFlag = append(tasksWithBlockedFlag, taskWithBlockedFlag{task, isTaskBlocked(board, task)})
	}

	writeJSONResponse(responseWriter, http.StatusOK, struct {
		ID      string                `json:"id"`
		Name    string                `json:"name"`
		Tasks   []taskWithBlockedFlag `json:"tasks"`
		Columns []string              `json:"columns"`
	}{board.ID, board.Name, tasksWithBlockedFlag, columnIDs})
}

// Same format as newId() in web/app.js, bumped until it's unused so a fast loop of creates can't clash
func generateUniqueID(prefix string, isIDTaken func(string) bool) string {
	for milliseconds := time.Now().UnixMilli(); ; milliseconds++ {
		candidateID := prefix + "-" + strconv.FormatInt(milliseconds, 36)
		if !isIDTaken(candidateID) {
			return candidateID
		}
	}
}

// Shown to the LLM as a 400, so it can fix its request
type badRequestError string

func (err badRequestError) Error() string { return string(err) }

type notFoundError string

func (err notFoundError) Error() string { return string(err) }

func decodeJSONRequestBody(responseWriter http.ResponseWriter, request *http.Request, destination any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(responseWriter, request.Body, maxLLMRequestBodyBytes)).Decode(destination); err != nil {
		http.Error(responseWriter, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeErrorResponse(responseWriter http.ResponseWriter, err error) {
	var badRequest badRequestError
	var notFound notFoundError
	switch {
	case errors.As(err, &badRequest):
		http.Error(responseWriter, err.Error(), http.StatusBadRequest)
	case errors.As(err, &notFound):
		http.Error(responseWriter, err.Error(), http.StatusNotFound)
	default:
		http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
	}
}

func handleCreateBoard(responseWriter http.ResponseWriter, request *http.Request) {
	var createBoardRequest struct {
		Name string `json:"name"`
	}
	if !decodeJSONRequestBody(responseWriter, request, &createBoardRequest) {
		return
	}

	var createdBoard Board
	err := updateBoardsInFile(func(boards []Board) ([]Board, error) {
		boardName := strings.TrimSpace(createBoardRequest.Name)
		if boardName == "" {
			return nil, badRequestError("name is required")
		}
		createdBoard = Board{
			ID: generateUniqueID("board", func(candidateID string) bool {
				return slices.ContainsFunc(boards, func(board Board) bool { return board.ID == candidateID })
			}),
			Name:  boardName,
			Tasks: []Task{},
		}
		return append(boards, createdBoard), nil
	})
	if err != nil {
		writeErrorResponse(responseWriter, err)
		return
	}

	writeJSONResponse(responseWriter, http.StatusCreated, createdBoard)
}

func handleCreateTicket(responseWriter http.ResponseWriter, request *http.Request) {
	var createTicketRequest struct {
		Title       string   `json:"title"`
		Description string   `json:"desc"`
		Column      string   `json:"column"`
		DueDate     string   `json:"dueDate"`
		BlockedBy   []string `json:"blockedBy"`
	}
	if !decodeJSONRequestBody(responseWriter, request, &createTicketRequest) {
		return
	}

	var createdTask Task
	err := updateBoardsInFile(func(boards []Board) ([]Board, error) {
		boardIndex := slices.IndexFunc(boards, func(board Board) bool { return board.ID == request.PathValue("id") })
		if boardIndex < 0 {
			return nil, notFoundError("board not found")
		}
		targetBoard := &boards[boardIndex]
		boardHasTask := func(taskID string) bool {
			return slices.ContainsFunc(targetBoard.Tasks, func(task Task) bool { return task.ID == taskID })
		}

		createdTask = Task{
			Title:       strings.TrimSpace(createTicketRequest.Title),
			Description: strings.TrimSpace(createTicketRequest.Description),
			Column:      createTicketRequest.Column,
			DueDate:     createTicketRequest.DueDate,
			CreatedAt:   time.Now().Format(time.DateOnly),
			BlockedBy:   []string{},
			Comments:    []Comment{},
		}
		if createdTask.Title == "" {
			return nil, badRequestError("title is required")
		}
		if createdTask.Column == "" {
			createdTask.Column = "backlog"
		}
		if !slices.Contains(columnIDs, createdTask.Column) {
			return nil, badRequestError("column must be one of: " + strings.Join(columnIDs, ", "))
		}
		if createdTask.DueDate != "" {
			if _, err := time.Parse(time.DateOnly, createdTask.DueDate); err != nil {
				return nil, badRequestError("dueDate must look like 2026-10-31")
			}
		}
		for _, blockerTaskID := range createTicketRequest.BlockedBy {
			if !boardHasTask(blockerTaskID) {
				return nil, badRequestError("blockedBy: no ticket " + blockerTaskID + " on this board")
			}
			createdTask.BlockedBy = append(createdTask.BlockedBy, blockerTaskID)
		}

		createdTask.ID = generateUniqueID("task", boardHasTask)
		targetBoard.Tasks = append(targetBoard.Tasks, createdTask)
		return boards, nil
	})
	if err != nil {
		writeErrorResponse(responseWriter, err)
		return
	}

	writeJSONResponse(responseWriter, http.StatusCreated, createdTask)
}
