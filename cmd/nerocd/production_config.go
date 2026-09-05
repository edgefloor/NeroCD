package main

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"

	"nerocd/internal/auth"
)

type deploymentMode string

const (
	modeDevelopment deploymentMode = "development"
	modeProduction  deploymentMode = "production"
)

func configuredMode() (deploymentMode, error) {
	mode := deploymentMode(strings.ToLower(strings.TrimSpace(os.Getenv("NEROCD_MODE"))))
	if mode == "" {
		mode = modeDevelopment
	}
	if mode != modeDevelopment && mode != modeProduction {
		return "", errors.New("NEROCD_MODE must be development or production")
	}
	return mode, nil
}

type productionDatabaseIdentity struct {
	url      *url.URL
	username string
	password string
	database string
	host     string
	port     string
}

func parseProductionDatabaseURL(value string) (productionDatabaseIdentity, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.User == nil {
		return productionDatabaseIdentity{}, errors.New("production database credentials are invalid")
	}
	username := strings.TrimSpace(parsed.User.Username())
	password, hasPassword := parsed.User.Password()
	database := strings.TrimPrefix(parsed.Path, "/")
	if username == "" || !hasPassword || password == "" || parsed.Hostname() == "" || database == "" || strings.Contains(database, "/") || parsed.Fragment != "" {
		return productionDatabaseIdentity{}, errors.New("production database credentials are invalid")
	}
	return productionDatabaseIdentity{url: parsed, username: username, password: password, database: database, host: strings.ToLower(parsed.Hostname()), port: productionDatabasePort(parsed)}, nil
}

func productionDatabasePort(parsed *url.URL) string {
	if port := parsed.Port(); port != "" {
		return port
	}
	return "5432"
}

// validateProductionCredentialPair keeps the migration owner and application
// principal separate while ensuring both target the same intended database.
// Its errors intentionally never include URL or credential material.
func validateProductionCredentialPair(ownerURL, appURL, configuredOwner, configuredApp string) error {
	owner, err := parseProductionDatabaseURL(ownerURL)
	if err != nil {
		return err
	}
	app, err := parseProductionDatabaseURL(appURL)
	if err != nil {
		return err
	}
	configuredOwner, configuredApp = strings.TrimSpace(configuredOwner), strings.TrimSpace(configuredApp)
	if configuredOwner == "" || configuredApp == "" || owner.username != configuredOwner || app.username != configuredApp {
		return errors.New("production database credential roles are invalid")
	}
	if owner.username == app.username || owner.password == app.password || owner.host != app.host || owner.port != app.port || owner.database != app.database {
		return errors.New("production database credentials must be distinct and target the same database")
	}
	return nil
}

func productionDatabaseURL() (string, error) {
	path := strings.TrimSpace(os.Getenv("NEROCD_DATABASE_URL_FILE"))
	if path == "" {
		return "", errors.New("production requires NEROCD_DATABASE_URL_FILE")
	}
	raw, err := readOwnerOnlyProductionSecret(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	identity, err := parseProductionDatabaseURL(value)
	if err != nil {
		return "", err
	}
	credential := strings.TrimSpace(os.Getenv("NEROCD_DATABASE_CREDENTIAL"))
	expected := ""
	switch credential {
	case "owner":
		expected = strings.TrimSpace(os.Getenv("NEROCD_OWNER_DATABASE_USER"))
	case "app":
		expected = strings.TrimSpace(os.Getenv("NEROCD_APP_DATABASE_USER"))
	default:
		return "", errors.New("production requires NEROCD_DATABASE_CREDENTIAL as owner or app")
	}
	if expected == "" || identity.username != expected {
		return "", errors.New("production database credential role is invalid")
	}
	return value, nil
}

func productionCredentialPairFromFiles() error {
	ownerPath := strings.TrimSpace(os.Getenv("NEROCD_OWNER_DATABASE_URL_FILE"))
	if ownerPath == "" {
		ownerPath = strings.TrimSpace(os.Getenv("NEROCD_DATABASE_URL_FILE"))
	}
	appPath := strings.TrimSpace(os.Getenv("NEROCD_APP_DATABASE_URL_FILE"))
	if ownerPath == "" || appPath == "" {
		return errors.New("production requires owner and application database secret files")
	}
	owner, err := readOwnerOnlyProductionSecret(ownerPath)
	if err != nil {
		return err
	}
	app, err := readOwnerOnlyProductionSecret(appPath)
	if err != nil {
		return err
	}
	return validateProductionCredentialPair(string(owner), string(app), os.Getenv("NEROCD_OWNER_DATABASE_USER"), os.Getenv("NEROCD_APP_DATABASE_USER"))
}

func loadDatabaseURL(mode deploymentMode) (string, error) {
	if mode == modeProduction {
		if err := validateProductionImageReference(strings.TrimSpace(os.Getenv("NEROCD_IMAGE_REF"))); err != nil {
			return "", err
		}
		if strings.TrimSpace(os.Getenv("NEROCD_DATABASE_URL")) != "" {
			return "", errors.New("production rejects NEROCD_DATABASE_URL; use NEROCD_DATABASE_URL_FILE")
		}
		return productionDatabaseURL()
	}
	value := strings.TrimSpace(os.Getenv("NEROCD_DATABASE_URL"))
	if value != "" && validateDatabaseURL(value) != nil {
		return "", errors.New("NEROCD_DATABASE_URL must be a postgres URL")
	}
	return value, nil
}

var productionImageDigest = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$`)

// validateProductionImageReference accepts only a canonical repository digest.
// It deliberately rejects both a mutable tag and tag@digest: the Compose
// profile must not have an alternate mutable name for a release artifact.
func validateProductionImageReference(value string) error {
	if value == "" {
		return errors.New("production requires NEROCD_IMAGE_REF")
	}
	if !productionImageDigest.MatchString(value) {
		return errors.New("production requires NEROCD_IMAGE_REF as repository@sha256 digest")
	}
	prefix := strings.SplitN(value, "@", 2)[0]
	if strings.LastIndex(prefix, ":") > strings.LastIndex(prefix, "/") {
		return errors.New("production rejects tagged NEROCD_IMAGE_REF")
	}
	return nil
}

func validateProductionOrigin(value string) error {
	origin, err := url.Parse(strings.TrimSpace(value))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil {
		return errors.New("production requires NEROCD_PUBLIC_ORIGIN as an HTTPS origin")
	}
	return nil
}

func loadOIDCProvider(mode deploymentMode, publicOrigin string) (auth.OIDCProvider, error) {
	issuer := strings.TrimSpace(os.Getenv("NEROCD_OIDC_ISSUER_URL"))
	clientID := strings.TrimSpace(os.Getenv("NEROCD_OIDC_CLIENT_ID"))
	secretPath := strings.TrimSpace(os.Getenv("NEROCD_OIDC_CLIENT_SECRET_FILE"))
	present := 0
	for _, value := range []string{issuer, clientID, secretPath} {
		if value != "" {
			present++
		}
	}
	if present == 0 {
		return nil, nil
	}
	if present != 3 {
		return nil, errors.New("OIDC configuration requires issuer URL, client ID, and client secret file")
	}
	allowLoopbackHTTP := mode == modeDevelopment
	if _, err := auth.ValidateOIDCIssuerURL(issuer, allowLoopbackHTTP); err != nil {
		return nil, err
	}
	origin, err := url.Parse(strings.TrimSpace(publicOrigin))
	if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("OIDC requires NEROCD_PUBLIC_ORIGIN as an exact origin")
	}
	if origin.Scheme != "https" && (!allowLoopbackHTTP || origin.Scheme != "http" || !oidcLoopbackHost(origin.Hostname())) {
		return nil, errors.New("OIDC public origin must use HTTPS or development loopback HTTP")
	}
	secret, err := readOwnerOnlyProductionSecret(secretPath)
	if err != nil || len(secret) > 64*1024 || strings.TrimSpace(string(secret)) == "" {
		return nil, errors.New("OIDC client secret cannot be read")
	}
	provider, err := auth.NewOIDCClient(auth.OIDCConfig{
		IssuerURL: issuer, ClientID: clientID, ClientSecret: strings.TrimSpace(string(secret)),
		RedirectURL: strings.TrimRight(origin.String(), "/") + "/api/v1/oidc/callback", AllowLoopbackHTTP: allowLoopbackHTTP,
	})
	if err != nil {
		return nil, err
	}
	return provider, nil
}

func oidcLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

// validateTrustedProxyCIDRs keeps doctor aligned with server startup without
// accepting an unbounded or malformed forwarding trust boundary. Values are
// deliberately not reported back: proxy topology is operational configuration.
func validateTrustedProxyCIDRs(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > 32 {
		return errors.New("NEROCD_TRUSTED_PROXY_CIDRS may contain at most 32 CIDRs")
	}
	for _, part := range parts {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil || !prefix.IsValid() {
			return errors.New("NEROCD_TRUSTED_PROXY_CIDRS must contain valid CIDRs")
		}
	}
	return nil
}

func productionDoctor() error {
	mode, err := configuredMode()
	if err != nil {
		return err
	}
	if mode == modeProduction {
		if err = validateProductionImageReference(strings.TrimSpace(os.Getenv("NEROCD_IMAGE_REF"))); err != nil {
			return err
		}
		if err = productionCredentialPairFromFiles(); err != nil {
			return err
		}
		if err = validateProductionOrigin(os.Getenv("NEROCD_PUBLIC_ORIGIN")); err != nil {
			return err
		}
		if err = validateTrustedProxyCIDRs(os.Getenv("NEROCD_TRUSTED_PROXY_CIDRS")); err != nil {
			return err
		}
	} else if _, err = loadDatabaseURL(mode); err != nil {
		return err
	}
	if mode == modeProduction && strings.EqualFold(strings.TrimSpace(os.Getenv("NEROCD_COOKIE_SECURE")), "false") {
		return errors.New("production rejects insecure cookies")
	}
	if _, err := loadOIDCProvider(mode, strings.TrimSpace(os.Getenv("NEROCD_PUBLIC_ORIGIN"))); err != nil {
		return err
	}
	fmt.Printf(`{"ok":true,"mode":%q,"database":"configured"}`+"\n", mode)
	return nil
}
