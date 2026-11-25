package main

// tw d1 execute's SQL: --command, --file, or stdin when neither is given
// and stdin is not a terminal (issue #523), as kv key put, queues send and
// secret put read stdin.

import (
	"fmt"
	"io"
	"os"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// d1Input is stdin and whether it is a terminal (a test replaces it).
var d1Input = func() (io.Reader, bool) { return os.Stdin, isTerminal() }

// d1ExecuteSQL is the SQL tw d1 execute runs: --command, --file, or stdin.
func d1ExecuteSQL(command, file string) (string, error) {
	switch {
	case command != "" && file != "":
		return "", fail("bad_request", "tw d1 execute takes --command <sql> or --file <file>, not both")
	case command != "":
		return command, nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fail("bad_request", err.Error())
		}
		return string(b), nil
	}
	in, tty := d1Input()
	if tty {
		return "", fail("bad_request", "tw d1 execute needs --command <sql>, --file <file>, or SQL on stdin").
			withFix("tw d1 execute <db> < file.sql")
	}
	b, err := io.ReadAll(io.LimitReader(in, mgmt.D1BodyLimit+1))
	if err != nil {
		return "", fail("bad_request", "reading the SQL from stdin: "+err.Error())
	}
	if len(b) > mgmt.D1BodyLimit {
		return "", fail("bad_request", fmt.Sprintf("the SQL on stdin is over %d bytes (8 MiB, the limit of one tw d1 execute)", mgmt.D1BodyLimit))
	}
	return string(b), nil
}
