package natives_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moriyoshi/oganesson/runtime/natives"
)

// TestPGConnectionOptionsDSN pins the DSN the pg shim builds (pg.js
// connectionString/withOptions) from node-postgres' discrete connection
// options. No database is contacted: a Pool's constructor only computes its
// DSN (_dsn). application_name, statement_timeout and
// idle_in_transaction_session_timeout become startup runtime parameters;
// connectionTimeoutMillis maps to libpq connect_timeout (seconds, rounded up);
// options passes through. A value already present in a connectionString wins.
func TestPGConnectionOptionsDSN(t *testing.T) {
	svc := natives.NewServices()
	defer svc.Postgres.Close()
	got := run(t, svc, nil, `
		import pg from "pg";
		const p = new pg.Pool({
			connectionString: "postgres://u@h:5432/db",
			application_name: "ogtest",
			statement_timeout: 4200,
			idle_in_transaction_session_timeout: 9000,
			connectionTimeoutMillis: 2500,
			options: "-c search_path=myschema",
		});
		globalThis.result = p._dsn;
	`)
	for _, want := range []string{
		"application_name=ogtest",
		"statement_timeout=4200",
		"idle_in_transaction_session_timeout=9000",
		"connect_timeout=3", // ceil(2500 / 1000)
		"options=-c%20search_path%3Dmyschema",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("DSN %q lacks %q", got, want)
		}
	}

	// A value already in the connectionString is kept, not duplicated.
	got2 := run(t, svc, nil, `
		import pg from "pg";
		globalThis.result = new pg.Pool({ connectionString: "postgres://u@h:5432/db?application_name=keep", application_name: "ignored" })._dsn;
	`)
	if strings.Count(got2, "application_name=") != 1 || !strings.Contains(got2, "application_name=keep") {
		t.Errorf("DSN %q should keep the connectionString's application_name, not append another", got2)
	}
}

// TestPGParseConfigAcceptsOptions verifies the other half: pgx accepts the
// mapped DSN (an unrecognised query parameter is not an error) and forwards the
// runtime parameters to the startup message, so the server actually applies
// them. ParseConfig parses without connecting, so this needs no server.
func TestPGParseConfigAcceptsOptions(t *testing.T) {
	dsn := "postgres://u:pw@127.0.0.1:5432/db?application_name=ogtest&statement_timeout=4200&idle_in_transaction_session_timeout=9000&connect_timeout=3&options=-c%20search_path%3Dmyschema"
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx rejects the mapped DSN: %v", err)
	}
	rp := cfg.ConnConfig.RuntimeParams
	for k, want := range map[string]string{
		"application_name":                    "ogtest",
		"statement_timeout":                   "4200",
		"idle_in_transaction_session_timeout": "9000",
	} {
		if rp[k] != want {
			t.Errorf("RuntimeParam %q = %q, want %q (pgx must forward it to startup)", k, rp[k], want)
		}
	}
	if cfg.ConnConfig.ConnectTimeout != 3*time.Second {
		t.Errorf("ConnectTimeout = %v, want 3s", cfg.ConnConfig.ConnectTimeout)
	}
}

// TestPGSSLVerifyDSN pins the DSN the pg shim builds for ssl: { ca }: the host
// registers a verifying TLS config and the shim carries its og-internal `ogtls`
// key alongside sslmode=require. An ssl without a ca keeps the encrypt-only
// baseline (no ogtls). No database is contacted.
func TestPGSSLVerifyDSN(t *testing.T) {
	svc := natives.NewServices()
	defer svc.Postgres.Close()
	_, _, caPEM := genCA(t, "pg ssl ca", 5)
	got := run(t, svc, map[string]any{"CA": caPEM}, `
		import pg from "pg";
		const p = new pg.Pool({ connectionString: "postgres://u@h:5432/db", ssl: { ca: CA, rejectUnauthorized: true } });
		globalThis.result = p._dsn;
	`)
	if !strings.Contains(got, "ogtls=og-") || !strings.Contains(got, "sslmode=require") {
		t.Errorf("DSN %q should carry ogtls and sslmode=require", got)
	}

	got2 := run(t, svc, nil, `
		import pg from "pg";
		globalThis.result = new pg.Pool({ connectionString: "postgres://u@h:5432/db", ssl: true })._dsn;
	`)
	if strings.Contains(got2, "ogtls=") {
		t.Errorf("ssl:true (no ca) should not register a CA: %q", got2)
	}
}
