# go-rar

Pure Go reader and writer for RAR 2.9-4.x archives. It stores files without
compression, with an API that mirrors `archive/zip`, so `zip.NewWriter` can
often become `rar.NewWriter`.

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

## Reading

```go
r, err := rar.OpenReader("chapter-01.cbr")
if err != nil {
	log.Fatal(err)
}
defer r.Close()
for _, f := range r.File {
	fmt.Println(f.Name, f.UnpackedSize)
}
page, err := fs.ReadFile(r, "001.jpg") // checks the CRC32 at EOF
if err != nil {
	log.Fatal(err)
}
fmt.Println(len(page))
```

## Limitations

- Writing: no compression, encryption, multi-volume, solid archives or RAR5.
  Each entry is buffered in memory until the next Create or Close.
- Reading: compressed entries are listed but `Open` returns `ErrAlgorithm`.
  Encrypted, split and RAR5 archives are rejected. The marker must be at
  offset 0, so SFX archives are not supported.

RAR and WinRAR are trademarks of RARLAB; this project is not affiliated with RARLAB.

MIT licensed, see [LICENSE](LICENSE).
