package sqlitezstd_test

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	_ "github.com/jtarchie/sqlitezstd"
	_ "github.com/mattn/go-sqlite3" // ensure you import the SQLite3 driver
	"github.com/onsi/gomega/gexec"
)

// nolint: gosec
func randomBoundingBox() (float64, float64, float64, float64) {
	minX := rand.Float64() * 100
	maxX := minX + rand.Float64()*10
	minY := rand.Float64() * 100
	maxY := minY + rand.Float64()*10

	return minX, maxX, minY, maxY
}

// nolint: gochecknoglobals
var dbPath, zstPath string

// setupDB prepares a database for benchmarking.
// It returns the path of the created database and a cleanup function.
// nolint: cyclop
func setupDB(b *testing.B) (string, string) {
	b.Helper()

	if dbPath != "" {
		return dbPath, zstPath
	}

	buildPath, err := os.MkdirTemp("", "")
	if err != nil {
		b.Fatalf("Failed to create temp directory: %v", err)
	}

	dbPath = filepath.Join(buildPath, "test.sqlite")

	client, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}

	_, err = client.Exec(`
		CREATE TABLE entries (
			value INTEGER,
			sentence TEXT,
			minX REAL,
			maxX REAL,
			minY REAL,
			maxY REAL
		);

		CREATE VIRTUAL TABLE demo_rtree USING rtree(
			id INTEGER PRIMARY KEY,
			minX REAL,
			maxX REAL,
			minY REAL,
			maxY REAL
		);
	`)
	if err != nil {
		b.Fatalf("Failed to create table: %v", err)
	}

	transaction, err := client.Begin()
	if err != nil {
		b.Fatalf("Failed to start transaction: %v", err)
	}

	defer func() { _ = transaction.Rollback() }()

	insertEntry, err := transaction.Prepare("INSERT INTO entries (value, sentence, minX, maxX, minY, maxY) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		b.Fatalf("Failed to entry prepare: %v", err)
	}
	defer insertEntry.Close() //nolint: errcheck

	slog.Info("insert.start")

	for range 1_000_000 {
		//nolint: gosec
		minX, maxX, minY, maxY := randomBoundingBox()
		_, err = insertEntry.Exec(rand.Int63(), gofakeit.Sentence(100), minX, maxX, minY, maxY)
		if err != nil {
			b.Fatalf("Failed to insert data: %v", err)
		}
	}

	slog.Info("insert.done")

	_ = transaction.Commit()

	slog.Info("tx.done")

	// index reduces number of page loads
	_, err = client.Exec(`
		CREATE INDEX aindex ON entries(value);
		CREATE VIRTUAL TABLE entries_porter USING fts5(sentence, tokenize="porter unicode61");
		INSERT INTO entries_porter(rowid, sentence)
			SELECT rowid, sentence FROM entries;
		INSERT INTO demo_rtree (id, minX, maxX, minY, maxY)
		 	SELECT rowid, minX, maxX, minY, maxY from entries;
		INSERT INTO entries_porter(entries_porter) VALUES ('optimize');
		PRAGMA page_size = 4096;
		VACUUM;
		PRAGMA optimize;
	`)
	if err != nil {
		b.Fatalf("Failed to create index: %v", err)
	}

	slog.Info("commit.end")

	// Assuming the compression step is the same as in the test,
	// and that it's already correctly set up and works.
	zstPath = dbPath + ".zst"

	command := exec.Command(
		"go", "run", "github.com/SaveTheRbtz/zstd-seekable-format-go/cmd/zstdseek",
		"-f", dbPath,
		"-o", zstPath,
		"-c", "16:32:64",
	)

	session, err := gexec.Start(command, os.Stderr, os.Stderr)
	if err != nil {
		b.Fatalf("Failed to compress data: %v", err)
	}

	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()

	select {
	case <-session.Exited:
		if session.ExitCode() != 0 {
			panic("something went wrong compressing")
		}
	case <-timeout.C:
		panic("something timeout wrong compressing")
	}

	slog.Info("compression.end")

	return dbPath, zstPath
}

// Benchmark reading from the uncompressed SQLite file.
func BenchmarkReadUncompressedSQLite(b *testing.B) {
	dbPath, _ := setupDB(b)

	client, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close() //nolint: errcheck

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT MAX(value) FROM entries").Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

func BenchmarkReadUncompressedRtreeSQLite(b *testing.B) {
	dbPath, _ := setupDB(b)

	client, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close() //nolint: errcheck

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow(`
				SELECT
					COUNT(*)
				FROM demo_rtree
				WHERE
					minX <= -1 AND 1 <= maxX AND 
					minY <= -1 AND 1 <= maxY 
			`).Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

func BenchmarkReadUncompressedSQLiteFTS5Porter(b *testing.B) {
	dbPath, _ := setupDB(b)

	client, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close() //nolint: errcheck

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT COUNT(*) FROM entries_porter WHERE entries_porter MATCH 'alligator OR work'").Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

// Benchmark reading from the compressed SQLite file.
func BenchmarkReadCompressedSQLite(b *testing.B) {
	_, zstPath := setupDB(b)

	client, err := sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd", zstPath))
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close() //nolint: errcheck

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT MAX(value) FROM entries").Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

func BenchmarkReadCompressedSQLiteFTS5Porter(b *testing.B) {
	_, zstPath := setupDB(b)

	client, err := sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd", zstPath))
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close() //nolint: errcheck

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT COUNT(*) FROM entries_porter WHERE entries_porter MATCH 'alligator OR work'").Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

func BenchmarkReadCompressedHTTPSQLite(b *testing.B) {
	_, zstPath := setupDB(b)

	zstDir := filepath.Dir(zstPath)

	server := httptest.NewServer(http.FileServer(http.Dir(zstDir)))
	defer server.Close()

	client, err := sql.Open("sqlite3", fmt.Sprintf("%s/%s?vfs=zstd", server.URL, filepath.Base(zstPath)))
	if err != nil {
		b.Fatalf("Query failed: %v", err)
	}
	defer client.Close() //nolint: errcheck

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT MAX(value) FROM entries").Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

func BenchmarkReadCompressedRtreeSQLite(b *testing.B) {
	_, zstPath := setupDB(b)

	client, err := sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd", zstPath))
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close() //nolint: errcheck

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow(`
				SELECT
					COUNT(*)
				FROM demo_rtree
				WHERE
					minX <= -1 AND 1 <= maxX AND 
					minY <= -1 AND 1 <= maxY 
			`).Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}
