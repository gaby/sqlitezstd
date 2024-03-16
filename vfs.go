package sqlitezstd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go"
	"github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
	"github.com/psanford/sqlite3vfs"
	"howett.net/ranger"
)

type ZstdVFS struct{}

var _ sqlite3vfs.VFS = &ZstdVFS{}

func (z *ZstdVFS) Access(name string, flags sqlite3vfs.AccessFlag) (bool, error) {
	if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-journal") {
		return false, nil
	}

	return true, nil
}

func (z *ZstdVFS) Delete(name string, dirSync bool) error {
	return sqlite3vfs.ReadOnlyError
}

func (z *ZstdVFS) FullPathname(name string) string {
	return name
}

func (z *ZstdVFS) Open(name string, flags sqlite3vfs.OpenFlag) (sqlite3vfs.File, sqlite3vfs.OpenFlag, error) {
	var (
		err    error
		reader io.ReadSeeker
		closer io.Closer
	)

	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		uri, err := url.Parse(name)
		if err != nil {
			return nil, 0, sqlite3vfs.CantOpenError
		}

		reader, err = ranger.NewReader(&ranger.HTTPRanger{URL: uri})
		if err != nil {
			return nil, 0, sqlite3vfs.CantOpenError
		}

		closer = io.NopCloser(reader)
	} else {
		reader, err = os.Open(name)
		if err != nil {
			return nil, 0, sqlite3vfs.CantOpenError
		}

		//nolint: forcetypeassert
		closer = reader.(io.Closer)
	}

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, 0, sqlite3vfs.CantOpenError
	}

	seekable, err := seekable.NewReader(reader, decoder)
	if err != nil {
		return nil, 0, sqlite3vfs.CantOpenError
	}

	return &ZstdFile{
		decoder:  decoder,
		closer:   closer,
		seekable: seekable,
	}, flags | sqlite3vfs.OpenReadOnly, nil
}

func Init() error {
	err := sqlite3vfs.RegisterVFS("zstd", &ZstdVFS{})
	if err != nil {
		return fmt.Errorf("could not register vfs: %w", err)
	}

	return nil
}
