// Offline synthetic-database marker for the isolated image upgrade rehearsal.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 3 || (args[0] != "seed" && args[0] != "check") {
		return fmt.Errorf("usage: storage-upgrade-db seed|check DATABASE MARKER")
	}
	if _, err := os.Stat(args[1]); err != nil {
		return fmt.Errorf("existing database required: %w", err)
	}
	mode := "ro"
	if args[0] == "seed" {
		mode = "rw"
	}
	db, err := sql.Open("sqlite", "file:"+args[1]+"?mode="+mode)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if args[0] == "seed" {
		if _, err := db.ExecContext(ctx, "CREATE TABLE upgrade_rehearsal_marker (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)"); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, "INSERT INTO upgrade_rehearsal_marker (id,value) VALUES (1,?)", args[2])
		return err
	}
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM upgrade_rehearsal_marker WHERE id=1").Scan(&value); err != nil {
		return err
	}
	if value != args[2] {
		return fmt.Errorf("restored database marker differs")
	}
	return nil
}
