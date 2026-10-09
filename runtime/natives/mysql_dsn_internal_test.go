package natives

import (
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
)

// TestMySQLDSNOptions verifies the Go half of the mysql2 connection options:
// mysqlDSN converts the mysql:// URL (with the query params js/mysql2.js'
// withOptions appends) to a go-sql-driver DSN whose recognised params the
// driver routes to config fields when it parses the DSN at sql.Open. Parsing
// the formatted DSN here (no server) confirms multiStatements and timeout
// survive the URL -> *mysql.Config -> FormatDSN -> ParseDSN round-trip as the
// MultiStatements and Timeout fields, not as session Params.
func TestMySQLDSNOptions(t *testing.T) {
	out, err := mysqlDSN("mysql://root@127.0.0.1:3306/og?multiStatements=true&timeout=7000ms&charset=utf8mb4")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := driver.ParseDSN(out)
	if err != nil {
		t.Fatalf("go-sql-driver rejects the formatted DSN %q: %v", out, err)
	}
	if !cfg.MultiStatements {
		t.Errorf("DSN %q: MultiStatements = false, want true", out)
	}
	if cfg.Timeout != 7*time.Second {
		t.Errorf("DSN %q: Timeout = %v, want 7s", out, cfg.Timeout)
	}
	if cfg.DBName != "og" || cfg.User != "root" || cfg.Addr != "127.0.0.1:3306" {
		t.Errorf("DSN %q: base fields not preserved (db=%q user=%q addr=%q)", out, cfg.DBName, cfg.User, cfg.Addr)
	}
}
