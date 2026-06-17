package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/scan"
)

var postgresPlaceholderRE = regexp.MustCompile(`\$\d+`)

type bobDB struct {
	*sql.DB
}

type bobTx struct {
	*sql.Tx
}

type generatedBobDB struct {
	*sql.DB
}

type generatedBobTx struct {
	*sql.Tx
}

func newBobDB(db *sql.DB) *bobDB {
	return &bobDB{DB: db}
}

func (db *bobDB) generated() *generatedBobDB {
	return &generatedBobDB{DB: db.DB}
}

func (db *generatedBobDB) QueryContext(ctx context.Context, query string, args ...any) (scan.Rows, error) {
	return db.DB.QueryContext(ctx, query, args...)
}

func (tx *bobTx) generated() *generatedBobTx {
	return &generatedBobTx{Tx: tx.Tx}
}

func (tx *generatedBobTx) QueryContext(ctx context.Context, query string, args ...any) (scan.Rows, error) {
	return tx.Tx.QueryContext(ctx, query, args...)
}

func (db *bobDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return bobQueryContext(ctx, db.DB, query, args...)
}

func (db *bobDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return bobQueryRowContext(ctx, db.DB, query, args...)
}

func (db *bobDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return bobExecContext(ctx, db.DB, query, args...)
}

func (db *bobDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*bobTx, error) {
	tx, err := db.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &bobTx{Tx: tx}, nil
}

func (db *bobDB) rawQueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.DB.QueryContext(ctx, query, args...)
}

func (db *bobDB) rawQueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.DB.QueryRowContext(ctx, query, args...)
}

func (db *bobDB) rawExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.DB.ExecContext(ctx, query, args...)
}

func (tx *bobTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return bobQueryContext(ctx, tx.Tx, query, args...)
}

func (tx *bobTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return bobQueryRowContext(ctx, tx.Tx, query, args...)
}

func (tx *bobTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return bobExecContext(ctx, tx.Tx, query, args...)
}

func bobQueryContext(ctx context.Context, exec interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args ...any) (*sql.Rows, error) {
	sqlQuery, sqlArgs, err := bobSQL(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return exec.QueryContext(ctx, sqlQuery, sqlArgs...)
}

func bobQueryRowContext(ctx context.Context, exec interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string, args ...any) *sql.Row {
	rawQuery, rawArgs, err := postgresPlaceholdersToBob(query, args...)
	if err != nil {
		panic(err)
	}
	sqlQuery, sqlArgs := bob.MustBuild(ctx, psql.RawQuery(rawQuery, rawArgs...))
	return exec.QueryRowContext(ctx, sqlQuery, sqlArgs...)
}

func bobExecContext(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) (sql.Result, error) {
	sqlQuery, sqlArgs, err := bobSQL(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return exec.ExecContext(ctx, sqlQuery, sqlArgs...)
}

func bobSQL(ctx context.Context, query string, args ...any) (string, []any, error) {
	rawQuery, rawArgs, err := postgresPlaceholdersToBob(query, args...)
	if err != nil {
		return "", nil, err
	}
	return bob.Build(ctx, psql.RawQuery(rawQuery, rawArgs...))
}

func postgresPlaceholdersToBob(query string, args ...any) (string, []any, error) {
	var err error
	rawArgs := make([]any, 0, len(args))
	rawQuery := postgresPlaceholderRE.ReplaceAllStringFunc(query, func(match string) string {
		if err != nil {
			return match
		}
		position, parseErr := strconv.Atoi(match[1:])
		if parseErr != nil {
			err = parseErr
			return match
		}
		if position < 1 || position > len(args) {
			err = fmt.Errorf("placeholder %s has no argument", match)
			return match
		}
		rawArgs = append(rawArgs, args[position-1])
		return "?"
	})
	if err != nil {
		return "", nil, err
	}
	return rawQuery, rawArgs, nil
}
