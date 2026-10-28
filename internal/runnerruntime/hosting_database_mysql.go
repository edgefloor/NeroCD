//go:build unix

package runnerruntime

import (
	"context"
	"database/sql"
	"net"
	"net/netip"
	"time"

	"github.com/go-sql-driver/mysql"
)

type databaseMySQLLogger struct{}

func (databaseMySQLLogger) Print(...interface{}) {}

// Readiness uses the saved app credential over the inspected private container
// address. Official image initialization creates both accounts from protected files;
// an existing account or failed authentication is never repaired by this probe.
func databaseMySQLConfig(address string, values []DatabaseCredentialValue) (*mysql.Config, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return nil, errDatabaseReadiness
	}
	config := mysql.NewConfig()
	config.User = "ncd_app"
	config.Passwd = string(databasePassword(values, "app"))
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(address, "3306")
	config.DBName = "app"
	config.Timeout = 2 * time.Second
	config.ReadTimeout = 2 * time.Second
	config.WriteTimeout = 2 * time.Second
	config.Logger = databaseMySQLLogger{}
	return config, nil
}

func databaseMySQLReady(ctx context.Context, address string, values []DatabaseCredentialValue) error {
	config, err := databaseMySQLConfig(address, values)
	if err != nil {
		return errDatabaseReadiness
	}
	connector, err := mysql.NewConnector(config)
	config.Passwd = ""
	if err != nil {
		return errDatabaseReadiness
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	var one int
	if db.QueryRowContext(ctx, "SELECT 1").Scan(&one) != nil || one != 1 {
		return errDatabaseReadiness
	}
	return nil
}
