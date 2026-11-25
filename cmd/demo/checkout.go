package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"coreadmin/internal/common/pgdb"
)

// Every checkout of the repo (the main one and each worktree) serves its
// demo on a port of its own, kept in one file in git's common directory, so
// every checkout and the Herdr worktrees bar read the same assignment: the
// main checkout 8080, a worktree the lowest free port from 8081 the first
// time it starts, and the same port from then on.
const (
	portsFile = "coreadmin-demo-ports.json" // {checkout path: port}
	mainPort  = 8080
)

// checkout is the checkout this demo was built from, as its pages show it.
type checkout struct {
	Name   string `json:"name"`   // the worktree's folder, or "main" for the main checkout
	Branch string `json:"branch"` // its git branch
	Port   int    `json:"port"`
	Main   bool   `json:"main"`
	path   string
}

func thisCheckout() (checkout, error) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	c := checkout{Name: pgdb.Checkout(), path: root}
	if c.Name == "" {
		c.Name, c.Main = "main", true
	}
	if out, err := exec.Command("git", "-C", root, "branch", "--show-current").Output(); err == nil {
		c.Branch = strings.TrimSpace(string(out))
	}
	port, err := demoPort(root, c.Main)
	c.Port = port
	return c, err
}

// demoPort is the checkout's port, assigning one the first time.
func demoPort(root string, main bool) (int, error) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return 0, fmt.Errorf("git common dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(strings.TrimSpace(string(out)), portsFile), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// Two demos starting at once mustn't take the same port.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		return 0, err
	}
	ports := map[string]int{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &ports); err != nil {
			return 0, fmt.Errorf("%s: %w", f.Name(), err)
		}
	}
	if p, ok := ports[root]; ok {
		return p, nil
	}
	p := mainPort
	if !main {
		taken := make([]int, 0, len(ports))
		for _, q := range ports {
			taken = append(taken, q)
		}
		// Not one another checkout has, nor one in use (a demo started with -addr).
		for p = mainPort + 1; slices.Contains(taken, p) || inUse(p); p++ {
		}
	}
	ports[root] = p
	data, err = json.MarshalIndent(ports, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := f.Truncate(0); err != nil {
		return 0, err
	}
	if _, err := f.WriteAt(append(data, '\n'), 0); err != nil {
		return 0, err
	}
	return p, nil
}

func inUse(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return true
	}
	l.Close()
	return false
}

// withCheckout serves the demo pages with the checkout they come from: a
// badge on every page (shared/checkout.js) and its name before the title,
// so tabs from different worktrees can't be mixed up.
func withCheckout(web fs.FS, c checkout, next http.Handler) http.Handler {
	info, _ := json.Marshal(c)
	tag := []byte(fmt.Sprintf(`<script>window.CHECKOUT=%s</script><script src="/shared/checkout.js"></script></body>`, info))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || strings.HasSuffix(name, "/") {
			name += "index.html"
		}
		if !strings.HasSuffix(name, ".html") {
			next.ServeHTTP(w, r)
			return
		}
		page, err := fs.ReadFile(web, name)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(bytes.Replace(page, []byte("</body>"), tag, 1))
	})
}
