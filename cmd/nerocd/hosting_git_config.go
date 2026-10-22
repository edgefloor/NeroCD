package main

import (
	"nerocd/internal/app"
	"os"
	"strings"
)

func loadHostingGitHosts() ([]string, error) {
	raw := os.Getenv("NEROCD_HOSTING_GIT_ALLOWED_HOSTS")
	hosts := []string{"github.com"}
	if raw != "" {
		hosts = strings.Split(raw, ",")
	}
	p, err := app.PublicHostingGitPolicy(hosts)
	return p.AllowedHosts, err
}
