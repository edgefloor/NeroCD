package main

import (
	"fmt"
	"net/http"
	"time"
)

var version = "fixture"

func main() {
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unready" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, "%s:%s", version, r.URL.Path)
	})}
	if err := server.ListenAndServe(); err != nil {
		panic(err)
	}
}
