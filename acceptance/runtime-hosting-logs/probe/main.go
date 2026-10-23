package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	start, burst := make(chan struct{}, 1), make(chan struct{}, 1)
	http.HandleFunc("/emit", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case start <- struct{}{}:
		default:
		}
		w.WriteHeader(200)
	})
	http.HandleFunc("/burst", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case burst <- struct{}{}:
		default:
		}
		w.WriteHeader(200)
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	go func() {
		<-start
		// A fixed, disposable test credential, deliberately split across writes.
		_, _ = fmt.Fprint(os.Stdout, "bearer ncr_runtime_logs_")
		time.Sleep(time.Millisecond)
		_, _ = fmt.Fprintln(os.Stdout, "fixture_bearer canary-safe")
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for n := 0; ; n++ {
			select {
			case <-ticker.C:
				_, _ = fmt.Fprintf(os.Stdout, "stdout line %d ☃\n", n)
				_, _ = fmt.Fprintf(os.Stderr, "stderr line %d\n", n)
			case <-burst:
				for i := 0; i < 1200; i++ {
					_, _ = fmt.Fprintf(os.Stdout, "retention burst %04d\n", i)
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()
	if http.ListenAndServe(":8080", nil) != nil {
		os.Exit(1)
	}
}
