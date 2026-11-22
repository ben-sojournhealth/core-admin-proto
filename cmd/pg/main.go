// Command pg manages the local Postgres everything runs on (see
// internal/common/pgdb): start it, stop it, or print how to connect.
//
//	go run ./cmd/pg start
//	go run ./cmd/pg stop
//	go run ./cmd/pg url
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"coreadmin/internal/common/pgdb"
)

func main() {
	cmd := "start"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "start":
		if _, err := pgdb.Pool(context.Background()); err != nil {
			fail(err)
		}
		fmt.Println("Postgres is running.")
		fmt.Println("demo:  ", pgdb.URL("coreadmin"))
		fmt.Println("tests: ", pgdb.URL("coreadmin_test"))
	case "stop":
		var ctl string
		for _, c := range []string{filepath.Join(pgdb.Dir(), "bin", "bin", "pg_ctl"), filepath.Join(pgdb.Dir(), "run", "bin", "pg_ctl")} {
			if _, err := os.Stat(c); err == nil {
				ctl = c
				break
			}
		}
		if ctl == "" {
			fail(fmt.Errorf("no pg_ctl under %s", pgdb.Dir()))
		}
		out, err := exec.Command(ctl, "stop", "-D", filepath.Join(pgdb.Dir(), "data"), "-m", "fast").CombinedOutput()
		fmt.Print(string(out))
		if err != nil {
			fail(err)
		}
	case "url":
		fmt.Println(pgdb.URL("coreadmin"))
	default:
		fail(fmt.Errorf("unknown command %q: start, stop or url", cmd))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
