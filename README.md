# PDF Unlock

Go program that finds encrypted PDFs and removes the password **in place**. Files that are already open are skipped. A password is requested only when at least one locked file is found. The same password is used for every locked PDF.

## Double-click

1. Place `pdfunlock.exe` in the folder you want to scan (subfolders are included).
2. Double-click `pdfunlock.exe`.
3. Review the list (`locked` vs `already open`).
4. If anything is locked, type the password:
   - **Enter** — unlock
   - **Backspace** — erase
   - **Tab** — show or hide the password
   - **Ctrl+U** — clear the line
   - **Ctrl+C** — cancel
5. Press Enter when finished to close the window.

Unlocked files **replace** the originals only after a successful decrypt.

## Command line

Requires [Go](https://go.dev/dl/) 1.24 or later.

```powershell
go build -o pdfunlock.exe .
go run .
go run . -dir path\to\pdfs
go run . -recursive=false
go run . -password "secret" file.pdf
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-dir` | current folder | Folder to scan when no paths are given |
| `-recursive` | `true` | Also scan subfolders |
| `-password` | prompt | Password for every locked PDF |

Extra arguments are files or folders to process instead of `-dir`.

To hardcode a password, set `defaultPassword` in `main.go` and rebuild.

## Windows file properties

`versioninfo.json` is not read at runtime. After `go generate`, the next `go build` stamps Explorer → Properties → Details. The versioninfo tool is pinned in `go.mod` (no extra install).

```powershell
go generate
go build -o pdfunlock.exe .
```

The `.syso` is gitignored. Unlocking works without this step.

## Notes

- Uses [pdfcpu](https://github.com/pdfcpu/pdfcpu) for decryption.
- Supports common PDF encryption, including AES-256.
