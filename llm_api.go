package main

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const maxLLMRequestBodyBytes = 1 << 20

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

// Requiring application/json forces browsers to send a CORS preflight, which this server never approves,
// so other websites can't create boards or tickets through the visitor's browser
func decodeJSONRequestBody(responseWriter http.ResponseWriter, request *http.Request, destination any) bool {
	if mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type")); mediaType != "application/json" {
		http.Error(responseWriter, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
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
