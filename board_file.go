package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

var boardFileMutex sync.Mutex

const maxBoardFileUploadBytes = 10 << 20

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
	var parsedBoardFile BoardFile
	decoder := json.NewDecoder(bytes.NewReader(uploadedBoardFile))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsedBoardFile); err != nil {
		http.Error(responseWriter, "invalid board file: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBoardFile(parsedBoardFile); err != nil {
		http.Error(responseWriter, "invalid board file: "+err.Error(), http.StatusBadRequest)
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
	if err := os.WriteFile(temporaryFilePath, boardFileContents, 0o600); err != nil {
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

// Same format as newId() in web/app.js. IDs end up inside HTML attributes, so nothing else is allowed.
var validIDPattern = regexp.MustCompile(`^[a-z]+-[a-z0-9]+$`)

// Checks every field the frontend puts into the page without escaping, so a crafted PUT can't inject HTML
func validateBoardFile(boardFile BoardFile) error {
	if len(boardFile.Boards) == 0 {
		return errors.New("needs at least one board")
	}
	for _, board := range boardFile.Boards {
		if !validIDPattern.MatchString(board.ID) {
			return fmt.Errorf("board id %q must look like board-abc123", board.ID)
		}
		for _, task := range board.Tasks {
			if !validIDPattern.MatchString(task.ID) {
				return fmt.Errorf("task id %q must look like task-abc123", task.ID)
			}
			if !slices.Contains(columnIDs, task.Column) {
				return fmt.Errorf("task %s: column must be one of: %s", task.ID, strings.Join(columnIDs, ", "))
			}
			if task.DueDate != "" {
				if _, err := time.Parse(time.DateOnly, task.DueDate); err != nil {
					return fmt.Errorf("task %s: dueDate must look like 2026-10-31", task.ID)
				}
			}
			if _, err := time.Parse(time.DateOnly, task.CreatedAt); err != nil {
				return fmt.Errorf("task %s: createdAt must look like 2026-10-31", task.ID)
			}
			for _, blockerTaskID := range task.BlockedBy {
				if !validIDPattern.MatchString(blockerTaskID) {
					return fmt.Errorf("task %s: blockedBy id %q must look like task-abc123", task.ID, blockerTaskID)
				}
			}
			for _, comment := range task.Comments {
				if !validIDPattern.MatchString(comment.ID) {
					return fmt.Errorf("task %s: comment id %q must look like comment-abc123", task.ID, comment.ID)
				}
				if _, err := time.Parse(time.RFC3339Nano, comment.CreatedAt); err != nil {
					return fmt.Errorf("task %s: comment createdAt must be an ISO 8601 timestamp", task.ID)
				}
			}
		}
	}
	return nil
}

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
