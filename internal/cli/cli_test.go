package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, err bytes.Buffer
	code = Run(args, &out, &err)
	return out.String(), err.String(), code
}

func TestVersionExact(t *testing.T) {
	stdout, stderr, code := run(t, "--version")
	if code != 0 {
		t.Fatalf("--version exit = %d, want 0", code)
	}
	if stdout != "pgschema 0.1.0\n" {
		t.Fatalf("--version stdout = %q, want %q", stdout, "pgschema 0.1.0\n")
	}
	var tele map[string]any
	if err := json.Unmarshal([]byte(stderr), &tele); err != nil {
		t.Fatalf("telemetry line is not JSON: %v (stderr=%q)", err, stderr)
	}
	if tele["tool"] != "pgschema" || tele["command"] != "version" || tele["ok"] != true {
		t.Errorf("telemetry = %v", tele)
	}
}

func TestVersionJSON(t *testing.T) {
	stdout, _, code := run(t, "version", "--json")
	if code != 0 {
		t.Fatalf("version --json exit = %d, want 0", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("version --json stdout is not JSON: %v", err)
	}
	if v["ok"] != true || v["version"] != "0.1.0" || v["tool"] != "pgschema" {
		t.Errorf("version --json = %v", v)
	}
}

func TestNoCommandIsUsageError(t *testing.T) {
	stdout, _, code := run(t)
	if code != 2 {
		t.Fatalf("no command exit = %d, want 2", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if v["ok"] != false {
		t.Errorf("ok = %v, want false", v["ok"])
	}
	errObj, _ := v["error"].(map[string]any)
	if errObj["code"] != "USAGE" {
		t.Errorf("error.code = %v, want USAGE", errObj["code"])
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	_, _, code := run(t, "frobnicate")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
}

func TestQueryRefusesDeleteWithoutTouchingDB(t *testing.T) {
	stdout, _, code := run(t, "query", "--dsn", "postgres://ro@127.0.0.1:1/db", "--sql", "DELETE FROM book WHERE id = 1")
	if code != 1 {
		t.Fatalf("refused query exit = %d, want 1", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	errObj, _ := v["error"].(map[string]any)
	if errObj["code"] != "READ_ONLY_REFUSED" {
		t.Errorf("error.code = %v, want READ_ONLY_REFUSED", errObj["code"])
	}
}

func TestLimitOver1000IsUsageError(t *testing.T) {
	_, _, code := run(t, "query", "--dsn", "postgres://ro@127.0.0.1:1/db", "--sql", "SELECT 1", "--limit", "1001")
	if code != 2 {
		t.Fatalf("limit 1001 exit = %d, want 2", code)
	}
}

func TestDSNRedactedInDBError(t *testing.T) {
	// Unreachable port, so this fails fast with a connection error.
	stdout, _, code := run(t, "describe", "--dsn", "postgres://ro:super-secret-pw@127.0.0.1:1/db?sslmode=disable")
	if code != 3 {
		t.Fatalf("unreachable DB exit = %d, want 3", code)
	}
	if strings.Contains(stdout, "super-secret-pw") {
		t.Fatalf("stdout leaks the DSN password: %q", stdout)
	}
	if !strings.Contains(stdout, "ro:***@") && !strings.Contains(stdout, "DB_UNREACHABLE") {
		t.Fatalf("stdout should be a redacted DB_UNREACHABLE error: %q", stdout)
	}
}

func TestDoctorWithoutDSN(t *testing.T) {
	stdout, _, code := run(t, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit = %d, want 0", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("doctor stdout is not JSON: %v", err)
	}
	if v["ok"] != true {
		t.Errorf("doctor ok = %v, want true", v["ok"])
	}
}

func TestDoctorUnreachableIsExit3(t *testing.T) {
	_, _, code := run(t, "doctor", "--dsn", "postgres://ro:pw123@127.0.0.1:1/db")
	if code != 3 {
		t.Fatalf("doctor with unreachable DSN exit = %d, want 3", code)
	}
}

func TestSelfTestSkipsWithoutDSN(t *testing.T) {
	t.Setenv("PGSCHEMA_TEST_DSN", "")
	stdout, _, code := run(t, "self-test")
	if code != 0 {
		t.Fatalf("self-test without DSN exit = %d, want 0", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("self-test stdout is not JSON: %v", err)
	}
	if v["skipped"] != true {
		t.Errorf("self-test skipped = %v, want true (%q)", v["skipped"], stdout)
	}
}

func TestRequestEnvelope(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "req.json")
	env := `{"protocol":"arabica-plugin-v1","command":"query","args":{"dsn":"postgres://ro@127.0.0.1:1/db","sql":"DELETE FROM book"}}`
	if err := os.WriteFile(req, []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := run(t, "--request", req)
	if code != 1 {
		t.Fatalf("--request envelope query exit = %d, want 1 (refused DELETE)", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	errObj, _ := v["error"].(map[string]any)
	if errObj["code"] != "READ_ONLY_REFUSED" {
		t.Errorf("error.code = %v, want READ_ONLY_REFUSED", errObj["code"])
	}
}

func TestRequestEnvelopeBadProtocol(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "req.json")
	if err := os.WriteFile(req, []byte(`{"protocol":"other-v9","command":"version","args":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, code := run(t, "--request", req)
	if code != 2 {
		t.Fatalf("bad protocol exit = %d, want 2", code)
	}
}
