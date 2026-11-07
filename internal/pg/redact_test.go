package pg

import "testing"

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			"postgres://ro:secret123@127.0.0.1:5432/db?sslmode=disable",
			"postgres://ro:***@127.0.0.1:5432/db?sslmode=disable",
		},
		{
			"postgres://ro@127.0.0.1:5432/db",
			"postgres://ro@127.0.0.1:5432/db",
		},
		{
			"postgres://user:p%40ss@db.example.com:5432/app",
			"postgres://user:***@db.example.com:5432/app",
		},
		{
			"host=127.0.0.1 user=ro password=hunter2 dbname=db",
			"host=127.0.0.1 user=ro password=*** dbname=db",
		},
		{
			"host=127.0.0.1 user=ro password='quoted secret' dbname=db",
			"host=127.0.0.1 user=ro password=*** dbname=db",
		},
		{
			"failed to connect to postgres://ro:s3cret@host/db: connection refused",
			"failed to connect to postgres://ro:***@host/db: connection refused",
		},
		{
			"no dsn here at all",
			"no dsn here at all",
		},
		{
			"",
			"",
		},
	}
	for _, c := range cases {
		if got := RedactDSN(c.in); got != c.want {
			t.Errorf("RedactDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
