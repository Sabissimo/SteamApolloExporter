// Package apollo locates Apollo/Sunshine installs and edits their apps.json.
package apollo

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Install is a detected Apollo (or Sunshine-family) host installation.
type Install struct {
	Name      string `json:"name"`
	Root      string `json:"root"`
	ConfigDir string `json:"configDir"`
	AppsFile  string `json:"appsFile"`
	Service   string `json:"service"`
	Running   bool   `json:"running"`
}

// CoversDir is where exported cover PNGs are written.
func (in Install) CoversDir() string { return filepath.Join(in.ConfigDir, "covers") }

// Known service names, in preference order. Any other service whose binary is
// sunshinesvc.exe is picked up too (forks such as Vibepollo).
var preferredServices = []string{"ApolloService", "SunshineService"}

// Detect finds installed hosts via their Windows services, since the uninstall
// registry entries often lack InstallLocation.
func Detect() ([]Install, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	names, err := m.ListServices()
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var out []Install
	for _, name := range names {
		s, err := m.OpenService(name)
		if err != nil {
			continue
		}
		cfg, cerr := s.Config()
		st, serr := s.Query()
		s.Close()
		if cerr != nil {
			continue
		}
		bin := strings.Trim(cfg.BinaryPathName, `" `)
		if !strings.EqualFold(filepath.Base(bin), "sunshinesvc.exe") {
			continue
		}
		root := filepath.Dir(filepath.Dir(bin)) // <root>\tools\sunshinesvc.exe
		in := Install{
			Name:      strings.TrimSuffix(name, "Service"),
			Root:      root,
			ConfigDir: filepath.Join(root, "config"),
			Service:   name,
			Running:   serr == nil && st.State == svc.Running,
		}
		in.AppsFile = appsFileFromConfig(in.ConfigDir)
		out = append(out, in)
	}
	rank := func(in Install) int {
		for i, n := range preferredServices {
			if strings.EqualFold(in.Service, n) {
				return i
			}
		}
		return len(preferredServices)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		return rank(out[i]) < rank(out[j])
	})
	if len(out) == 0 {
		return nil, errors.New("no Apollo or Sunshine service found")
	}
	return out, nil
}

// appsFileFromConfig honours a file_apps override in sunshine.conf/apollo.conf.
func appsFileFromConfig(configDir string) string {
	def := filepath.Join(configDir, "apps.json")
	for _, conf := range []string{"sunshine.conf", "apollo.conf"} {
		f, err := os.Open(filepath.Join(configDir, conf))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), "=")
			if !ok || strings.TrimSpace(k) != "file_apps" {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"`)
			if v == "" {
				continue
			}
			f.Close()
			if !filepath.IsAbs(v) {
				v = filepath.Join(configDir, v)
			}
			return v
		}
		f.Close()
	}
	return def
}

// Apps is a loaded apps.json. Unknown fields are preserved on save.
type Apps struct {
	path string
	doc  map[string]any
}

// Load reads apps.json, creating an empty document if it doesn't exist.
func Load(path string) (*Apps, error) {
	a := &Apps{path: path, doc: map[string]any{"apps": []any{}, "env": map[string]any{}}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&a.doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if _, ok := a.doc["apps"].([]any); !ok {
		a.doc["apps"] = []any{}
	}
	return a, nil
}

func (a *Apps) list() []any { return a.doc["apps"].([]any) }

var steamIDRe = regexp.MustCompile(`steam://(?:rungameid|run)/(\d+)`)

// steamAppID returns the Steam app ID an entry launches, or "".
func steamAppID(entry map[string]any) string {
	var cmds []string
	if s, ok := entry["cmd"].(string); ok {
		cmds = append(cmds, s)
	}
	if d, ok := entry["detached"].([]any); ok {
		for _, v := range d {
			if s, ok := v.(string); ok {
				cmds = append(cmds, s)
			}
		}
	}
	for _, c := range cmds {
		if m := steamIDRe.FindStringSubmatch(c); m != nil {
			return m[1]
		}
	}
	return ""
}

// SteamEntries maps Steam app ID -> entry name for every app that launches a Steam game.
func (a *Apps) SteamEntries() map[string]string {
	out := map[string]string{}
	for _, v := range a.list() {
		if e, ok := v.(map[string]any); ok {
			if id := steamAppID(e); id != "" {
				name, _ := e["name"].(string)
				out[id] = name
			}
		}
	}
	return out
}

// Count returns the number of app entries.
func (a *Apps) Count() int { return len(a.list()) }

// AddSteamGame appends an entry launching the given Steam app. It is a no-op
// when an entry for that app already exists.
func (a *Apps) AddSteamGame(appID, name, imagePath string) bool {
	if _, exists := a.SteamEntries()[appID]; exists {
		return false
	}
	entry := map[string]any{
		"uuid":         newUUID(),
		"name":         name,
		"cmd":          "steam://rungameid/" + appID,
		"image-path":   imagePath,
		"auto-detach":  true,
		"wait-all":     true,
		"exit-timeout": 5,
	}
	a.doc["apps"] = append(a.list(), entry)
	return true
}

// RemoveSteamGame deletes every entry launching the given Steam app and
// returns the image paths those entries used.
func (a *Apps) RemoveSteamGame(appID string) (removed int, images []string) {
	var kept []any
	for _, v := range a.list() {
		e, ok := v.(map[string]any)
		if ok && steamAppID(e) == appID {
			removed++
			if img, _ := e["image-path"].(string); img != "" {
				images = append(images, img)
			}
			continue
		}
		kept = append(kept, v)
	}
	if kept == nil {
		kept = []any{}
	}
	a.doc["apps"] = kept
	return removed, images
}

// Save writes apps.json atomically, keeping the previous version as apps.json.bak.
func (a *Apps) Save() error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	if err := enc.Encode(a.doc); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(a.path); err == nil {
		if err := os.WriteFile(a.path+".bak", old, 0o644); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// RestartService restarts the host so it picks up apps.json changes.
func RestartService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer s.Close()

	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil && st.State != svc.StopPending {
			return fmt.Errorf("stop %s: %w", name, err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			st, err = s.Query()
			if err != nil {
				return err
			}
			if st.State == svc.Stopped {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out waiting for %s to stop", name)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	return nil
}
