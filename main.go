package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	httpPort = ":8080"
	tcpPort  = ":8081"
	dataFile = "kvstore.json"
	syncInterval = 5 * time.Second
)

type KeyValueStore struct {
	mu    sync.RWMutex
	store map[string]string
	dirty bool
}

func NewKeyValueStore() (*KeyValueStore, error) {
	kvs := &KeyValueStore{
		store: make(map[string]string),
	}
	
	if err := kvs.loadFromDisk(); err != nil {
		return nil, err
	}
	
	return kvs, nil
}

func (kvs *KeyValueStore) Set(key, value string) {
	kvs.mu.Lock()
	defer kvs.mu.Unlock()
	kvs.store[key] = value
	kvs.dirty = true
}

func (kvs *KeyValueStore) Get(key string) (string, bool) {
	kvs.mu.RLock()
	defer kvs.mu.RUnlock()
	value, ok := kvs.store[key]
	return value, ok
}

func (kvs *KeyValueStore) Count() int {
	kvs.mu.RLock()
	defer kvs.mu.RUnlock()
	return len(kvs.store)
}

func (kvs *KeyValueStore) loadFromDisk() error {
	file, err := os.Open(dataFile)
	if os.IsNotExist(err) {
		return nil // File doesn't exist, start with empty store
	} else if err != nil {
		return err
	}
	defer file.Close()

	return json.NewDecoder(file).Decode(&kvs.store)
}

func (kvs *KeyValueStore) saveToDisk() error {
	kvs.mu.Lock()
	defer kvs.mu.Unlock()

	if !kvs.dirty {
		return nil // No changes to save
	}

	tempFile := dataFile + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return err
	}

	if err := json.NewEncoder(file).Encode(kvs.store); err != nil {
		file.Close()
		return err
	}

	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempFile, dataFile); err != nil {
		return err
	}

	kvs.dirty = false
	return nil
}

func (kvs *KeyValueStore) startSyncRoutine(ctx context.Context) {
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := kvs.saveToDisk(); err != nil {
				log.Printf("Error saving to disk: %v", err)
			}
		case <-ctx.Done():
			if err := kvs.saveToDisk(); err != nil {
				log.Printf("Error saving to disk during shutdown: %v", err)
			}
			return
		}
	}
}

func main() {
	kvs, err := NewKeyValueStore()
	if err != nil {
		log.Fatalf("Error creating key-value store: %v", err)
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go kvs.startSyncRoutine(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/set", kvs.handleSet)
	mux.HandleFunc("/get", kvs.handleGet)
	mux.HandleFunc("/count", kvs.handleCount)

	server := &http.Server{Addr: httpPort, Handler: mux}

	// Start the HTTP server in a goroutine
	go func() {
		fmt.Printf("HTTP server starting on http://localhost%s\n", httpPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Start TCP listener for shutdown in a goroutine
	go func() {
		listener, err := net.Listen("tcp", tcpPort)
		if err != nil {
			log.Fatalf("TCP listener error: %v", err)
		}
		defer listener.Close()
		fmt.Printf("TCP shutdown listener started on port%s\n", tcpPort)

		_, err = listener.Accept()
		if err != nil {
			log.Printf("TCP accept error: %v", err)
			return
		}
		fmt.Println("Shutdown signal received via TCP")
		gracefulShutdown(server)
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("Shutdown signal received")
	cancel() // Stop the sync routine
	gracefulShutdown(server)
}

type SetRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type GetResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type CountResponse struct {
	Count int `json:"count"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func (kvs *KeyValueStore) handleSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSONResponse(w, ErrorResponse{Error: "Method not allowed"}, http.StatusMethodNotAllowed)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		sendJSONResponse(w, ErrorResponse{Error: "Error reading request body"}, http.StatusBadRequest)
		return
	}

	var req SetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		sendJSONResponse(w, ErrorResponse{Error: "Error parsing JSON"}, http.StatusBadRequest)
		return
	}

	if req.Key == "" {
		sendJSONResponse(w, ErrorResponse{Error: "Missing key"}, http.StatusBadRequest)
		return
	}

	kvs.Set(req.Key, req.Value)
	sendJSONResponse(w, map[string]string{"status": "OK"}, http.StatusOK)
}

func (kvs *KeyValueStore) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONResponse(w, ErrorResponse{Error: "Method not allowed"}, http.StatusMethodNotAllowed)
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		sendJSONResponse(w, ErrorResponse{Error: "Missing key"}, http.StatusBadRequest)
		return
	}

	value, ok := kvs.Get(key)
	if !ok {
		sendJSONResponse(w, ErrorResponse{Error: "Key not found"}, http.StatusNotFound)
		return
	}

	response := GetResponse{
		Key:   key,
		Value: value,
	}
	sendJSONResponse(w, response, http.StatusOK)
}

func (kvs *KeyValueStore) handleCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendJSONResponse(w, ErrorResponse{Error: "Method not allowed"}, http.StatusMethodNotAllowed)
		return
	}

	count := kvs.Count()
	response := CountResponse{Count: count}
	sendJSONResponse(w, response, http.StatusOK)
}

func sendJSONResponse(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

func gracefulShutdown(server *http.Server) {
	fmt.Println("Server is shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	fmt.Println("Server exiting")
	os.Exit(0)
}