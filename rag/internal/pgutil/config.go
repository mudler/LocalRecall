package pgutil

import (
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mudler/xlog"
	"strconv"
	"strings"
)

const defaultPoolMaxConns = int32(4)

func ApplyPoolLimits(config *pgxpool.Config, getenv func(string) string) {
	if HasPoolMaxConns(config.ConnString()) {
		return
	}
	config.MaxConns = defaultPoolMaxConns
	if v := getenv("POSTGRES_POOL_MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 {
			xlog.Warn("Ignoring invalid POSTGRES_POOL_MAX_CONNS", "value", v)
			return
		}
		config.MaxConns = int32(n)
	}
}
func HasPoolMaxConns(connString string) bool {
	cfg, err := pgconn.ParseConfig(connString)
	if err != nil {
		return false
	}
	_, ok := cfg.RuntimeParams["pool_max_conns"]
	return ok
}
func ApplyConnTimeouts(config *pgxpool.Config, getenv func(string) string) {
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	set := func(param, env, def string) {
		v := def
		if e := getenv(env); e != "" {
			v = e
		}
		if v == "" || v == "0" || strings.EqualFold(v, "off") {
			return
		}
		config.ConnConfig.RuntimeParams[param] = v
	}
	set("lock_timeout", "POSTGRES_LOCK_TIMEOUT", "30s")
	set("idle_in_transaction_session_timeout", "POSTGRES_IDLE_IN_TRANSACTION_TIMEOUT", "300s")
	set("statement_timeout", "POSTGRES_STATEMENT_TIMEOUT", "")
}
func FormatVector(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = fmt.Sprintf("%.6f", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
