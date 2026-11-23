package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
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

	machineHostname, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	allowedHostnames := []string{"localhost", strings.ToLower(machineHostname), strings.ToLower(machineHostname) + ".local"}

	log.Printf("data in %s", *boardFilePath)
	if *enableLLMAPI {
		go listenAndServe("LLM API", listenHost, *llmAPIPort, accessDescription, rejectUnknownHosts(allowedHostnames, llmAPIRouter))
	}
	listenAndServe("board", listenHost, *boardPort, accessDescription, rejectUnknownHosts(allowedHostnames, boardRouter))
}

func listenAndServe(serverName, listenHost string, listenPort int, accessDescription string, handler http.Handler) {
	log.Printf("%s listening on port %d (%s)", serverName, listenPort, accessDescription)
	server := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", listenHost, listenPort),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Fatal(server.ListenAndServe())
}

// Blocks DNS rebinding: a malicious website can point its own domain at this machine, but the browser
// still sends that domain as the Host, so only IP addresses and this machine's own names are let through
func rejectUnknownHosts(allowedHostnames []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestHostname := request.Host
		if hostnameWithoutPort, _, err := net.SplitHostPort(request.Host); err == nil {
			requestHostname = hostnameWithoutPort
		}
		requestHostname = strings.ToLower(strings.Trim(requestHostname, "[]"))
		if net.ParseIP(requestHostname) == nil && !slices.Contains(allowedHostnames, requestHostname) {
			http.Error(responseWriter, "unknown host "+request.Host+": use localhost, an IP address or this machine's name", http.StatusForbidden)
			return
		}
		next.ServeHTTP(responseWriter, request)
	})
}

// 0 when the LLM API is off
func handleGetLLMAPIPort(responseWriter http.ResponseWriter, request *http.Request) {
	if !*enableLLMAPI {
		writeJSONResponse(responseWriter, http.StatusOK, 0)
		return
	}
	writeJSONResponse(responseWriter, http.StatusOK, *llmAPIPort)
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
