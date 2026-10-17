# go-rar

Pure Go writer for RAR 2.9-4.x archives. It stores files without compression,
with an API that mirrors `archive/zip`, so `zip.NewWriter` can often become
`rar.NewWriter`.

CBR pages are already-compressed images, so compression would gain little, and
RAR4 is read by far more readers than RAR5.

## Install

```sh
go get github.com/elboletaire/go-rar
```

## Usage

```go
w := rar.NewWriter(f) // f is an *os.File for chapter-01.cbr
for i, page := range pages {
	fw, err := w.Create(fmt.Sprintf("%03d.jpg", i+1))
	if err != nil {
		log.Fatal(err)
	}
	if _, err := fw.Write(page); err != nil {
		log.Fatal(err)
	}
}
if err := w.Close(); err != nil {
	log.Fatal(err)
}
```

## Limitations

- No compression, encryption, multi-volume, solid archives or RAR5.
- Each entry is buffered in memory until the next Create or Close.
- Reading is planned for the next phase.

RAR and WinRAR are trademarks of RARLAB; this project is not affiliated with RARLAB.

MIT licensed, see [LICENSE](LICENSE).
