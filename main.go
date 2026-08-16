package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"golang.org/x/term"
)

// defaultPassword is used when -password is not set and nothing is typed at the prompt.
// Fill this in if every locked PDF uses the same password.
const defaultPassword = ""

func main() {
	pause := len(os.Args) == 1
	if pause {
		if exe, err := os.Executable(); err == nil {
			dir := filepath.Dir(exe)
			if strings.EqualFold(filepath.Base(dir), "unlock-pdf") {
				dir = filepath.Dir(dir)
			}
			_ = os.Chdir(dir)
		}
	}

	code := 0
	defer func() {
		if pause {
			fmt.Print("\nPress Enter to close...")
			_, _ = bufio.NewReader(os.Stdin).ReadBytes('\n')
		}
		os.Exit(code)
	}()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		code = 1
	}
}

func run() error {
	dir := flag.String("dir", ".", "folder to scan for PDFs")
	recursive := flag.Bool("recursive", true, "include PDFs in subfolders (default true; use -recursive=false to disable)")
	password := flag.String("password", "", "password used for every locked PDF")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] [files-or-folders...]\n\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(flag.CommandLine.Output(), "Detects encrypted PDFs and unlocks them in place with a shared password.")
		fmt.Fprintln(flag.CommandLine.Output(), "Double-click the .exe to scan the folder it lives in, including subfolders.")
		fmt.Fprintln(flag.CommandLine.Output())
		flag.PrintDefaults()
	}
	flag.Parse()

	pw := *password
	if pw == "" {
		pw = defaultPassword
	}

	targets := flag.Args()
	if len(targets) == 0 {
		targets = []string{*dir}
	}

	cwd, _ := os.Getwd()
	scope := "this folder only"
	if *recursive {
		scope = "this folder and subfolders"
	}
	fmt.Printf("PDF Unlock\nScanning: %s\n(%s)\n\n", cwd, scope)

	pdfs, err := collectPDFs(targets, *recursive)
	if err != nil {
		return err
	}
	if len(pdfs) == 0 {
		fmt.Println("No PDF files found. Password not needed.")
		return nil
	}

	locked := make([]string, 0)
	openFiles := make([]string, 0)
	var inspectFailed int
	for _, path := range pdfs {
		ok, err := isEncrypted(path)
		if err != nil {
			fmt.Printf("  FAIL  %s: %v\n", displayPath(path, cwd), err)
			inspectFailed++
			continue
		}
		if ok {
			fmt.Printf("  locked       %s\n", displayPath(path, cwd))
			locked = append(locked, path)
			continue
		}
		fmt.Printf("  already open %s\n", displayPath(path, cwd))
		openFiles = append(openFiles, path)
	}
	fmt.Println()

	if len(locked) == 0 {
		fmt.Println("No locked PDFs found. Password not needed.")
		if inspectFailed > 0 {
			return fmt.Errorf("%d file(s) failed", inspectFailed)
		}
		return nil
	}

	fmt.Printf("%d PDF(s) to unlock. Enter the password.\n\n", len(locked))

	if pw == "" {
		pw, err = askPassword()
		if err != nil {
			return err
		}
	}

	var unlocked, failed int
	i := 0
	for i < len(locked) {
		path := locked[i]
		if err := unlockPDF(path, pw); err != nil {
			if isPasswordError(err) && *password == "" && defaultPassword == "" {
				fmt.Println("That password did not work. Try again.")
				pw, err = askPassword()
				if err != nil {
					return err
				}
				continue
			}
			fmt.Printf("FAIL  %s: %v\n", path, err)
			failed++
			i++
			continue
		}
		fmt.Printf("UNLOCKED  %s\n", path)
		unlocked++
		i++
	}

	failed += inspectFailed
	fmt.Printf("\ndone: unlocked=%d skipped=%d failed=%d\n", unlocked, len(openFiles), failed)
	if failed > 0 {
		return fmt.Errorf("%d file(s) failed", failed)
	}
	return nil
}

func askPassword() (string, error) {
	for {
		fmt.Println("Type the password. Enter = unlock, Backspace = erase, Tab = show/hide, Ctrl+U = clear, Ctrl+C = cancel")
		pw, err := promptPassword()
		if err != nil {
			return "", err
		}
		if pw != "" {
			return pw, nil
		}
		fmt.Println("Password cannot be empty. Try again.")
	}
}

func promptPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		return readPasswordMasked(fd)
	}

	fmt.Print("PDF password: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", errors.New("no password entered")
	}
	return strings.TrimRight(scanner.Text(), "\r"), nil
}

func readPasswordMasked(fd int) (string, error) {
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, old)

	const label = "PDF password: "
	var runes []rune
	show := false
	width := paintPassword(label, runes, show, 0)

	in := bufio.NewReader(os.Stdin)
	for {
		r, _, err := in.ReadRune()
		if err != nil {
			return "", err
		}
		switch r {
		case '\r', '\n':
			fmt.Print("\r\n")
			return string(runes), nil
		case 0x03: // Ctrl+C
			fmt.Print("\r\n")
			return "", errors.New("cancelled")
		case '\t': // Tab toggles show/hide
			show = !show
			width = paintPassword(label, runes, show, width)
		case 0x7f, 0x08: // Backspace
			if len(runes) > 0 {
				runes = runes[:len(runes)-1]
				width = paintPassword(label, runes, show, width)
			}
		case 0x15: // Ctrl+U
			runes = runes[:0]
			width = paintPassword(label, runes, show, width)
		case 0x1b: // Esc / extra key sequence
			drainKeySequence(in)
		default:
			if r == 0 || r == 0xe0 {
				drainKeySequence(in)
				continue
			}
			if unicode.IsControl(r) {
				continue
			}
			runes = append(runes, r)
			width = paintPassword(label, runes, show, width)
		}
	}
}

func paintPassword(label string, runes []rune, show bool, prevWidth int) int {
	shown := strings.Repeat("*", len(runes))
	if show {
		shown = string(runes)
	}
	line := label + shown
	fmt.Print("\r" + line)
	if prevWidth > len(line) {
		fmt.Print(strings.Repeat(" ", prevWidth-len(line)))
		fmt.Print("\r" + line)
	}
	return len(line)
}

func drainKeySequence(in *bufio.Reader) {
	for in.Buffered() > 0 {
		next, _ := in.Peek(1)
		if len(next) == 0 {
			return
		}
		if next[0] == '[' || next[0] == 'O' || (next[0] >= '0' && next[0] <= '9') || next[0] == ';' {
			_, _, _ = in.ReadRune()
			continue
		}
		if next[0] >= 0x40 && next[0] <= 0x7e {
			_, _, _ = in.ReadRune()
		}
		return
	}
}

func collectPDFs(targets []string, recursive bool) ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	add := func(path string) error {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if _, ok := seen[abs]; ok {
			return nil
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
		return nil
	}

	for _, target := range targets {
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !isPDF(target) {
				return nil, fmt.Errorf("not a PDF: %s", target)
			}
			if err := add(target); err != nil {
				return nil, err
			}
			continue
		}

		if recursive {
			err = filepath.WalkDir(target, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					fmt.Printf("  skip  %s: %v\n", path, walkErr)
					if d != nil && d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if d.IsDir() || !isPDF(path) {
					return nil
				}
				return add(path)
			})
			if err != nil {
				return nil, err
			}
			continue
		}

		entries, err := os.ReadDir(target)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !isPDF(entry.Name()) {
				continue
			}
			if err := add(filepath.Join(target, entry.Name())); err != nil {
				return nil, err
			}
		}
	}

	return out, nil
}

func isPDF(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".pdf")
}

func displayPath(path, root string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

func isEncrypted(path string) (bool, error) {
	return hasEncryptDict(path)
}

func hasEncryptDict(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	const key = "/Encrypt"
	overlap := len(key) - 1
	prev := make([]byte, 0, overlap)
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if len(prev) > 0 {
				chunk = append(append([]byte{}, prev...), chunk...)
			}
			if encryptNameAt(chunk) {
				return true, nil
			}
			if n >= overlap {
				prev = append(prev[:0], buf[n-overlap:n]...)
			} else {
				prev = append(prev[:0], buf[:n]...)
			}
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

func encryptNameAt(data []byte) bool {
	key := []byte("/Encrypt")
	for {
		i := bytes.Index(data, key)
		if i < 0 {
			return false
		}
		next := byte(0)
		if i+len(key) < len(data) {
			next = data[i+len(key)]
		}
		if !pdfNameContinues(next) {
			return true
		}
		data = data[i+len(key):]
	}
}

func pdfNameContinues(b byte) bool {
	switch b {
	case 0, 9, 10, 12, 13, 32, '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return false
	}
	return b >= '!' && b <= '~'
}

func isPasswordError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pdfcpu.ErrWrongPassword) ||
		errors.Is(err, pdfcpu.ErrEncrypted) ||
		errors.Is(err, pdfcpu.ErrOwnerPasswordRequired) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "password") || strings.Contains(msg, "encrypted")
}

func unlockPDF(path, password string) error {
	tmp := path + ".unlocked.tmp"

	conf := model.NewDefaultConfiguration()
	conf.UserPW = password
	conf.OwnerPW = password

	if err := api.DecryptFile(path, tmp, conf); err != nil {
		os.Remove(tmp)
		return err
	}

	if err := os.Remove(path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace original: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("pdf decrypted to %s but could not restore original name: %w", tmp, err)
	}
	return nil
}
