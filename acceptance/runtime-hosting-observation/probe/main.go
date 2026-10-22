package main

import (
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if http.ListenAndServe(":8080", nil) != nil {
		os.Exit(1)
	}
}
