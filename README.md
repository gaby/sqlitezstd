# SQLiteZSTD: Read-Only Access to Compressed SQLite Files

## Description

SQLiteZSTD provides a tool to access SQLite databases that have been compressed
with
[Zstandard seekable (zstd)](https://github.com/facebook/zstd/blob/216099a73f6ec19c246019df12a2877dada45cca/contrib/seekable_format/zstd_seekable_compression_format.md)
in a Read-Only manner. Its functionality is based on
[SQLite3 Virtual File System (VFS) in Go](https://github.com/psanford/sqlite3vfs).

Please note, SQLiteZSTD is specifically designed for reading data; **it does not
support write operations**.

## Features

1. Read-only access to Zstd compressed SQLite databases.
2. Interface through SQLite3 VFS.
3. Compressed database is seekable facilitating ease of access.

## Usage

Your database needs to be compressed in the seekable format for zstd. I
recommend using this [CLI](github.com/SaveTheRbtz/zstd-seekable-format-go):

```bash
go get -a github.com/SaveTheRbtz/zstd-seekable-format-go/...
go run github.com/SaveTheRbtz/zstd-seekable-format-go/cmd/zstdseek \
	-f <dbPath> \
	-o <dbPath>.zst
```

The CLI provided different options for levels of compression. I have no
recommendations on best usage patterns.

Below is an example of how to use SQLiteZSTD in a Go program:

```go
import (
	sqlitezstd "github.com/jtarchie/sqlitezstd"
)

initErr := sqlitezstd.Init()
if initErr != nil {
	panic(fmt.Sprintf("Failed to initialize SQLiteZSTD: %s", initErr))
}

db, err := sql.Open("sqlite3", "<path-to-your-file>?vfs=zstd&mode=ro&immutable=true&synchronous=off")
if err != nil {
	panic(fmt.Sprintf("Failed to open database: %s", err))
}
```

In this Go code example:

1. The SQLiteZSTD library is initialized first with `sqlitezstd.Init()`.
2. An SQL connection to a compressed SQLite database is then established with
   `sql.Open()`.

The `sql.Open()` function takes as a parameter the path to the compressed SQLite
database, appended with a query string. Key query string parameters include:

- `vfs=zstd`: ensures the ZSTD VFS is used.
- `mode=ro`: opens the database in read-only mode.
- `immutable=true`: ensures the database is protected from any accidental write
  operations.
- `synchronous=off`: disables SQLite's disk synchronization for improved
  performance on read-heavy operations.