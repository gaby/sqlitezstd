package sqlitezstd_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/georgysavva/scany/v2/sqlscan"
	sqlitezstd "github.com/jtarchie/sqlitezstd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

func TestSqliteZstd(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SqliteZstd Suite")
}

func createDatabase() string {
	buildPath, err := os.MkdirTemp("", "")
	Expect(err).ToNot(HaveOccurred())

	dbPath := filepath.Join(buildPath, "test.sqlite")

	client, err := sql.Open("sqlite3", dbPath)
	Expect(err).ToNot(HaveOccurred())

	_, err = client.Exec(`
		CREATE TABLE entries (
			id INTEGER PRIMARY KEY
		);
	`)
	Expect(err).ToNot(HaveOccurred())

	for id := 1; id <= 1000; id++ {
		_, err = client.Exec("INSERT INTO entries (id) VALUES (?)", id)
		Expect(err).ToNot(HaveOccurred())
	}

	zstPath := dbPath + ".zst"

	command := exec.Command(
		"go", "run", "github.com/SaveTheRbtz/zstd-seekable-format-go/cmd/zstdseek",
		"-f", dbPath,
		"-o", zstPath,
	)

	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())
	Eventually(session).Should(gexec.Exit(0))

	return zstPath
}

func createComplexDatabase() (string, string) {
	buildPath, err := os.MkdirTemp("", "")
	Expect(err).ToNot(HaveOccurred())

	dbPath := filepath.Join(buildPath, "complex.sqlite")

	client, err := sql.Open("sqlite3", dbPath)
	Expect(err).ToNot(HaveOccurred())
	defer client.Close()

	_, err = client.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			name TEXT,
			age INTEGER
		);
		CREATE TABLE orders (
			id INTEGER PRIMARY KEY,
			user_id INTEGER,
			product TEXT,
			quantity INTEGER,
			FOREIGN KEY (user_id) REFERENCES users(id)
		);
	`)
	Expect(err).ToNot(HaveOccurred())

	for i := 1; i <= 10_000; i++ {
		_, err = client.Exec("INSERT INTO users (name, age) VALUES (?, ?)", fmt.Sprintf("User%d", i), 20+(i%60))
		Expect(err).ToNot(HaveOccurred())

		_, err = client.Exec("INSERT INTO orders (user_id, product, quantity) VALUES (?, ?, ?)", i, fmt.Sprintf("Product%d", i%100), i%10+1)
		Expect(err).ToNot(HaveOccurred())
	}

	zstPath := dbPath + ".zst"

	command := exec.Command(
		"go", "run", "github.com/SaveTheRbtz/zstd-seekable-format-go/cmd/zstdseek",
		"-f", dbPath,
		"-o", zstPath,
	)

	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	Expect(err).ToNot(HaveOccurred())
	Eventually(session).Should(gexec.Exit(0))

	return dbPath, zstPath
}

var _ = Describe("SqliteZSTD", func() {
	BeforeEach(func() {
		err := sqlitezstd.Init()
		Expect(err).ToNot(HaveOccurred())
	})

	It("can read from a compressed sqlite db", func() {
		zstPath := createDatabase()

		client, err := sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd", zstPath))
		Expect(err).ToNot(HaveOccurred())
		defer client.Close()

		row := client.QueryRow("SELECT COUNT(*) FROM entries;")
		Expect(row.Err()).ToNot(HaveOccurred())

		var count int64
		err = row.Scan(&count)
		Expect(err).ToNot(HaveOccurred())
		Expect(count).To(BeEquivalentTo(1000))
	})

	It("can handle multiple readers", func() {
		zstPath := createDatabase()

		waiter := &sync.WaitGroup{}

		for range 5 {
			waiter.Add(1)

			go func() {
				defer waiter.Done()
				defer GinkgoRecover()

				client, err := sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd", zstPath))
				Expect(err).ToNot(HaveOccurred())
				defer client.Close()

				for range 1_000 {
					row := client.QueryRow("SELECT * FROM entries ORDER BY RANDOM() LIMIT 1;")
					Expect(row.Err()).ToNot(HaveOccurred())
				}
			}()
		}

		waiter.Wait()
	})

	When("file does not exist", func() {
		It("returns an error", func() {
			client, err := sql.Open("sqlite3", "file:some.db?vfs=zstd")
			Expect(err).ToNot(HaveOccurred())
			defer client.Close()

			row := client.QueryRow("SELECT * FROM entries ORDER BY RANDOM() LIMIT 1;")
			Expect(row.Err()).To(HaveOccurred())
		})
	})

	It("allows reading from HTTP server", func() {
		zstPath := createDatabase()
		zstDir := filepath.Dir(zstPath)
		server := httptest.NewServer(http.FileServer(http.Dir(zstDir)))
		defer server.Close()

		client, err := sql.Open("sqlite3", fmt.Sprintf("%s/%s?vfs=zstd", server.URL, filepath.Base(zstPath)))
		Expect(err).ToNot(HaveOccurred())
		defer client.Close()

		row := client.QueryRow("SELECT COUNT(*) FROM entries;")
		Expect(row.Err()).ToNot(HaveOccurred())

		var count int64
		err = row.Scan(&count)
		Expect(err).ToNot(HaveOccurred())
		Expect(count).To(BeEquivalentTo(1000))
	})

	It("ensures data integrity between compressed and uncompressed databases", func() {
		uncompressedPath, compressedPath := createComplexDatabase()

		uncompressedDB, err := sql.Open("sqlite3", uncompressedPath)
		Expect(err).ToNot(HaveOccurred())
		defer uncompressedDB.Close()

		compressedDB, err := sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd", compressedPath))
		Expect(err).ToNot(HaveOccurred())
		defer compressedDB.Close()

		query := `
			SELECT u.age, COUNT(*) as order_count, SUM(o.quantity) as total_quantity
			FROM users u
			JOIN orders o ON u.id = o.user_id
			GROUP BY u.age
			ORDER BY u.age
		`

		type Result struct {
			Age           int
			OrderCount    int64
			TotalQuantity int64
		}

		var uncompressedResults, compressedResults []Result

		err = sqlscan.Select(context.Background(), uncompressedDB, &uncompressedResults, query)
		Expect(err).ToNot(HaveOccurred())

		err = sqlscan.Select(context.Background(), compressedDB, &compressedResults, query)
		Expect(err).ToNot(HaveOccurred())

		Expect(len(compressedResults)).To(BeNumerically(">", 0))
		Expect(len(compressedResults)).To(Equal(len(uncompressedResults)), "Compressed and uncompressed databases have different number of rows")

		for i := range uncompressedResults {
			Expect(compressedResults[i]).To(Equal(uncompressedResults[i]), "Row %d does not match between compressed and uncompressed databases", i)
		}
	})
})
