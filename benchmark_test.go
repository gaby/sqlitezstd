package sqlitezstd_test

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	sqlitezstd "github.com/jtarchie/sqlitezstd"
	_ "github.com/mattn/go-sqlite3" // ensure you import the SQLite3 driver
	"github.com/onsi/gomega/gexec"
	"github.com/pioz/faker"
)

//nolint: gosec
func randomBoundingBox() (float64, float64, float64, float64) {
	minX := rand.Float64() * 100
	maxX := minX + rand.Float64()*10
	minY := rand.Float64() * 100
	maxY := minY + rand.Float64()*10

	return minX, maxX, minY, maxY
}

//nolint: gochecknoglobals
var dbPath, zstPath string

// setupDB prepares a database for benchmarking.
// It returns the path of the created database and a cleanup function.
//nolint: cyclop
func setupDB(b *testing.B) (string, string) {
	b.Helper()

	if dbPath != "" {
		return dbPath, zstPath
	}

	_ = sqlitezstd.Init()

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
			sentence TEXT
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

	insertEntry, err := transaction.Prepare("INSERT INTO entries (value, sentence) VALUES (?, ?)")
	if err != nil {
		b.Fatalf("Failed to entry prepare: %v", err)
	}
	defer insertEntry.Close()

	insertRtree, err := transaction.Prepare("INSERT INTO demo_rtree (id, minX, maxX, minY, maxY) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		b.Fatalf("Failed to rtree prepare: %v", err)
	}
	defer insertRtree.Close()

	for index := range 1_000_000 {
		//nolint: gosec
		_, err = insertEntry.Exec(rand.Int63(), faker.Sentence())
		if err != nil {
			b.Fatalf("Failed to insert data: %v", err)
		}

		if index%10 == 0 {
			minX, maxX, minY, maxY := randomBoundingBox()

			_, err = insertRtree.Exec(index, minX, maxX, minY, maxY)
			if err != nil {
				b.Fatalf("Failed to insert data: %v", err)
			}
		}
	}

	_ = transaction.Commit()

	// index reduces number of page loads
	_, err = client.Exec(`
		CREATE INDEX aindex ON entries(value);
		CREATE VIRTUAL TABLE entries_porter USING fts5(sentence, tokenize="porter unicode61");
		CREATE VIRTUAL TABLE entries_trigram USING fts5(sentence, tokenize="trigram");
		INSERT INTO entries_porter(rowid, sentence)
			SELECT rowid, sentence FROM entries;
		INSERT INTO entries_trigram(rowid, sentence)
			SELECT rowid, sentence FROM entries;
		INSERT INTO entries_porter(entries_porter) VALUES ('optimize');
		INSERT INTO entries_trigram(entries_trigram) VALUES ('optimize');
		PRAGMA page_size = 65536;
		VACUUM;
	`)
	if err != nil {
		b.Fatalf("Failed to create index: %v", err)
	}

	// Assuming the compression step is the same as in the test,
	// and that it's already correctly set up and works.
	zstPath = dbPath + ".zst"

	command := exec.Command(
		"go", "run", "github.com/SaveTheRbtz/zstd-seekable-format-go/cmd/zstdseek",
		"-f", dbPath,
		"-o", zstPath,
		"-q", "7",
		"-c", "16:32:64",
	)

	session, err := gexec.Start(command, os.Stderr, os.Stderr)
	if err != nil {
		b.Fatalf("Failed to compress data: %v", err)
	}

	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()

	select {
	case <-session.Exited:
		if session.ExitCode() != 0 {
			panic("something went wrong compressing")
		}
	case <-timeout.C:
		panic("something timeout wrong compressing")
	}

	return dbPath, zstPath
}

// Benchmark reading from the uncompressed SQLite file.
func BenchmarkReadUncompressedSQLite(b *testing.B) {
	dbPath, _ := setupDB(b)

	client, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

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
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

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
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT COUNT(*) FROM entries_porter WHERE entries_porter MATCH 'alligator'").Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

func BenchmarkReadUncompressedSQLiteFTS5Trigram(b *testing.B) {
	dbPath, _ := setupDB(b)

	client, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT COUNT(*) FROM entries_trigram WHERE entries_trigram MATCH 'alligator'").Scan(&count)
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
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

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
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT COUNT(*) FROM entries_porter WHERE entries_porter MATCH 'alligator'").Scan(&count)
			if err != nil {
				b.Fatalf("Query failed: %v", err)
			}
		}
	})
}

func BenchmarkReadCompressedSQLiteFTS5Trigram(b *testing.B) {
	_, zstPath := setupDB(b)

	client, err := sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd", zstPath))
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

	client.SetMaxOpenConns(max(4, runtime.NumCPU()))

	b.ResetTimer() // Start timing now.

	b.RunParallel(func(pb *testing.PB) {
		var count int
		for pb.Next() {
			err = client.QueryRow("SELECT COUNT(*) FROM entries_trigram WHERE entries_trigram MATCH 'alligator'").Scan(&count)
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
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

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
	defer client.Close()

	_, err = client.Exec(`
		pragma temp_store = memory;
		pragma mmap_size = 268435456; -- 256 MB
		PRAGMA cache_size = 2000;
		PRAGMA busy_timeout = 5000;
	`)
	if err != nil {
		b.Fatalf("could not setup pragmas: %v", err)
	}

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
