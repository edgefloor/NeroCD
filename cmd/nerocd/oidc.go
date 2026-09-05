package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/store"
)

func oidcProvision(args []string) error {
	fs := flag.NewFlagSet("oidc-provision", flag.ContinueOnError)
	issuer := fs.String("issuer", "", "exact OIDC issuer URL")
	subject := fs.String("subject", "", "stable provider subject")
	email := fs.String("email", "", "new local user's email")
	name := fs.String("name", "", "new local user's display name")
	userID := fs.String("user-id", "", "existing local user ID to bind without mutation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadRuntimeConfig(":8080")
	if err != nil {
		return err
	}
	if cfg.databaseURL == "" {
		return errors.New("oidc-provision requires a configured PostgreSQL database")
	}
	validatedIssuer, err := auth.ValidateOIDCIssuerURL(*issuer, cfg.mode == modeDevelopment)
	if err != nil {
		return err
	}
	service, closeStore, err := newService(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer closeStore()
	user, err := service.ProvisionOIDCIdentity(context.Background(), app.OIDCProvisionInput{Issuer: validatedIssuer, Subject: *subject, Email: *email, Name: *name, UserID: *userID})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			return errors.New("OIDC identity or local user already exists")
		case errors.Is(err, store.ErrNotFound):
			return errors.New("local user was not found")
		default:
			return err
		}
	}
	verb := "provisioned"
	if strings.TrimSpace(*userID) != "" {
		verb = "bound"
	}
	fmt.Printf("%s OIDC identity for local user %s\n", verb, user.ID)
	return nil
}
