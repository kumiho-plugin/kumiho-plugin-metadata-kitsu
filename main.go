package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kumiho-plugin/kumiho-plugin-metadata-kitsu/plugin"
	"github.com/kumiho-plugin/kumiho-plugin-sdk/service"
	sdktypes "github.com/kumiho-plugin/kumiho-plugin-sdk/types"
)

func main() {
	host := envOrDefault("KUMIHO_PLUGIN_HOST", "127.0.0.1")
	port := envOrDefault("KUMIHO_PLUGIN_PORT", "8080")
	accessToken := envOrDefault("KITSU_ACCESS_TOKEN", "")
	refreshToken := envOrDefault("KITSU_REFRESH_TOKEN", "")

	provider := plugin.New("", accessToken, refreshToken, &http.Client{Timeout: 10 * time.Second})

	mux := http.NewServeMux()
	mux.HandleFunc(service.PathSearch, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", service.StatusMethodNotAllowed)
			return
		}

		var req sdktypes.SearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", service.StatusBadRequest)
			return
		}

		resp, err := provider.Search(r.Context(), &req)
		if err != nil {
			http.Error(w, err.Error(), service.StatusInternalError)
			return
		}
		writeJSON(w, resp)
	})

	mux.HandleFunc(service.PathFetch, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", service.StatusMethodNotAllowed)
			return
		}

		var req sdktypes.FetchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", service.StatusBadRequest)
			return
		}

		resp, err := provider.Fetch(r.Context(), &req)
		if err != nil {
			http.Error(w, err.Error(), service.StatusInternalError)
			return
		}
		writeJSON(w, resp)
	})

	mux.HandleFunc(service.PathHealth, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", service.StatusMethodNotAllowed)
			return
		}

		resp, err := provider.Healthcheck(r.Context())
		if err != nil {
			http.Error(w, err.Error(), service.StatusInternalError)
			return
		}
		writeJSON(w, resp)
	})

	mux.HandleFunc(service.PathManifest, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", service.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, provider.Manifest())
	})

	addr := host + ":" + port
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("kitsu plugin listening on %s", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set(service.HeaderContentType, service.ContentTypeJSON)
	w.WriteHeader(service.StatusOK)
	_ = json.NewEncoder(w).Encode(value)
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
