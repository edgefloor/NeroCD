package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unready" {
			w.WriteHeader(503)
			return
		}
		out := map[string]string{}
		for _, entry := range os.Environ() {
			name, value, ok := strings.Cut(entry, "=")
			if ok && strings.HasPrefix(name, "APP_") {
				sum := sha256.Sum256([]byte(value))
				out[name] = hex.EncodeToString(sum[:])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	if http.ListenAndServe(":8080", nil) != nil {
		os.Exit(1)
	}
}
