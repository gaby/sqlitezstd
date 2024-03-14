package sqlitezstd_test

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	sqlitezstd "github.com/jtarchie/sqlitezstd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

func TestSqliteZstd(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SqliteZstd Suite")
}

var _ = Describe("SqliteZSTD", func() {
	BeforeEach(func() {
		err := sqlitezstd.Init()
		Expect(err).ToNot(HaveOccurred())
	})

	It("can read from a compressed sqlite db", func() {
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

		client, err = sql.Open("sqlite3", fmt.Sprintf("%s?vfs=zstd&mode=ro&immutable=true&synchronous=off", zstPath))
		Expect(err).ToNot(HaveOccurred())
		defer client.Close()

		row := client.QueryRow("SELECT COUNT(*) FROM entries;")
		Expect(row.Err()).ToNot(HaveOccurred())

		var count int64
		err = row.Scan(&count)
		Expect(err).ToNot(HaveOccurred())
		Expect(count).To(BeEquivalentTo(1000))
	})
})