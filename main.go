package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"github.com/alecthomas/chroma/formatters/html"
	"github.com/alecthomas/chroma/lexers"
	"github.com/alecthomas/chroma/styles"
	"github.com/spf13/pflag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func logf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stdout, format+"\n", args...)
}

type annotateFS struct {
	grep *regexp.Regexp
	base http.Dir
}

var _ http.FileSystem = (*annotateFS)(nil)

type annFile struct {
	buf  *bytes.Buffer
	all  []byte // for Seek(0, io.SeekStart)
	orig http.File
}

func (a *annFile) Close() error {
	return a.orig.Close()
}

func (a *annFile) Read(p []byte) (n int, err error) {
	return a.buf.Read(p)
}

func (a *annFile) Seek(offset int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		return 0, errors.New("only SeekStart is implemented")
	}
	a.buf = bytes.NewBuffer(a.all[offset:])
	return 0, nil
}

func (a *annFile) Readdir(count int) ([]fs.FileInfo, error) {
	return nil, errors.New("unimplemented")
}

type fileInfo struct {
	fs.FileInfo
	size int64
}

func (i *fileInfo) Size() int64 {
	return i.size
}

func (a *annFile) Stat() (fs.FileInfo, error) {
	s, err := a.orig.Stat()
	if err != nil {
		return nil, err
	}
	return &fileInfo{FileInfo: s, size: int64(len(a.all))}, nil
}

var _ http.File = (*annFile)(nil)

func (fs *annotateFS) Open(name string) (http.File, error) {
	f, err := fs.base.Open(name)
	if err != nil {
		return nil, err
	}

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if stat.IsDir() || !strings.HasSuffix(name, ".go") {
		return f, nil
	}

	// `fs.base` is going to be the abs path to the module root. Let's say we
	// have the following situation:
	// - module root: /home/user/go/src/github.com/user/module
	// - file:        /home/user/go/src/github.com/user/module/foo/bar/baz.go
	// In that case, `name` will be /foo/bar/baz.go. From now on, we want it to
	// be `foo/bar/baz.go` instead (path relative to the module root) so that we
	// can effortlessly match with the output of `go build`.
	name = filepath.Join(".", name)

	// It's a Go file. Try to annotate it.
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	pkgDir := filepath.Dir(name) // 'foo/bar'
	cmd := exec.Command("go", "build", "-gcflags", "-m -m", "./"+pkgDir)
	cmd.Dir = string(fs.base)
	annotations, err := cmd.CombinedOutput()
	if err != nil {
		logf("%s: %v\n%v", cmd, err, string(annotations))
		return nil, err
	}

	m, err := processLines(annotations)
	if err != nil {
		return nil, err
	}

	modContents, err := zipFile(fs.grep, b, name, m)
	lexer := lexers.Get("go")
	style := styles.Monokai
	formatter := html.New(
		html.Standalone(true),
		html.WithClasses(true),
	)
	it, err := lexer.Tokenise(nil, modContents)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := formatter.Format(&out, style, it); err != nil {
		return nil, err
	}
	return &annFile{
		buf:  &out,
		all:  out.Bytes(),
		orig: f,
	}, nil
}

func main() {
	// Define command-line flags
	addr := pflag.String("addr", "localhost:9876", "Address to listen on")
	grep := pflag.String("grep", `(?m)^.*escapes to heap:(?:\n\s+.*)*`, "Show only annotations matching this regexp")
	flag.Parse()

	// Get the directory to serve
	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	cmd.Dir = dir
	modRoot, err := cmd.Output()
	if err != nil {
		logf("error getting module root: %v", err)
		os.Exit(1)
	}
	dir = strings.TrimSpace(string(modRoot))
	// Get the absolute path of the directory
	dir, err = filepath.Abs(dir)
	if err != nil {
		log.Fatalf("Error getting absolute path: %v", err)
	}

	// Set up the HTTP server
	http.Handle("/", noCacheHandler(http.FileServer(&annotateFS{
		grep: regexp.MustCompile(*grep),
		base: http.Dir(dir),
	})))

	// Start the server
	fmt.Printf("serving %s on http://%s\n", dir, *addr)
	err = http.ListenAndServe(*addr, nil)
	if err != nil {
		logf("error starting: %v", err)
		os.Exit(1)
	}
}

type line struct {
	file string
	line int
}

type pos int

func processLines(data []byte) (map[line]map[pos]string, error) {
	result := make(map[line]map[pos]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))

	// Regex to match lines with file:line:char prefix
	lineRegex := regexp.MustCompile(`^([^:]+):(\d+):(\d+): (.*)$`)

	for scanner.Scan() {
		text := scanner.Text()

		if matches := lineRegex.FindStringSubmatch(text); matches != nil {
			fileStr := matches[1]
			lineNum, err := strconv.Atoi(matches[2])
			if err != nil {
				return nil, errors.New("invalid line number in: " + text)
			}
			posNum, err := strconv.Atoi(matches[3])
			if err != nil {
				return nil, errors.New("invalid line pos in: " + text)
			}
			annotation := matches[4]
			key := line{file: filepath.Clean(fileStr), line: lineNum}

			if _, ok := result[key]; !ok {
				result[key] = map[pos]string{}
			}

			p := pos(posNum)
			if existing, ok := result[key][p]; ok {
				result[key][p] = existing + "\n" + annotation
			} else {
				result[key][p] = annotation
			}
		}
	}

	return result, nil
}

func noCacheHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "")
		w.Header().Set("Last-Modified", "")

		// Set Cache-Control headers to prevent caching
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", time.Unix(0, 0).Format(http.TimeFormat))

		h.ServeHTTP(w, r)
	})
}

func zipFile(re *regexp.Regexp, b []byte, file string, m map[line]map[pos]string) (string, error) {
	var result strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(b))
	lineNumber := 1

	for scanner.Scan() {
		originalLine := scanner.Text()
		result.WriteString(originalLine)
		result.WriteString("\n")

		key := line{file: file, line: lineNumber}
		if mm, exists := m[key]; exists {
			for _, annotation := range mm {
				if !re.MatchString(annotation) {
					continue
				}
				result.WriteString("/**********\n")
				result.WriteString(re.FindString(annotation))
				result.WriteString("\n***********/\n")
			}
		}

		lineNumber++
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading file: %v", err)
	}
	return result.String(), nil
}
