package db

import "embed"

//go:embed migrations/*.sql seeds/*.sql
var Files embed.FS
