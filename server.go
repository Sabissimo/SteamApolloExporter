package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"

	"github.com/Sabissimo/SteamApolloExporter/internal/apollo"
	"github.com/Sabissimo/SteamApolloExporter/internal/steam"
)

type server struct {
	ui fs.FS
	mu sync.Mutex // serialises apps.json edits
}

func newServer(ui fs.FS) *server { return &server{ui: ui} }

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(s.ui))
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /cover/{appid}", s.handleCover)
	mux.HandleFunc("POST /api/apply", s.handleApply)
	return mux
}

type gameView struct {
	steam.Game
	InApollo   bool   `json:"inApollo"`
	ApolloName string `json:"apolloName,omitempty"`
	HasCover   bool   `json:"hasCover"`
}

type stateView struct {
	Elevated    bool             `json:"elevated"`
	SteamRoot   string           `json:"steamRoot"`
	Libraries   []string         `json:"libraries"`
	SteamError  string           `json:"steamError,omitempty"`
	Installs    []apollo.Install `json:"installs"`
	ApolloError string           `json:"apolloError,omitempty"`
	Target      *apollo.Install  `json:"target,omitempty"`
	AppsCount   int              `json:"appsCount"`
	Games       []gameView       `json:"games"`
}

// target picks the install matching the requested service, else the default.
func target(installs []apollo.Install, service string) *apollo.Install {
	for i := range installs {
		if strings.EqualFold(installs[i].Service, service) {
			return &installs[i]
		}
	}
	if len(installs) > 0 {
		return &installs[0]
	}
	return nil
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	st := stateView{Elevated: windows.GetCurrentProcessToken().IsElevated(), Games: []gameView{}}

	installs, err := apollo.Detect()
	if err != nil {
		st.ApolloError = err.Error()
	}
	st.Installs = installs
	st.Target = target(installs, r.URL.Query().Get("target"))

	var existing map[string]string
	if st.Target != nil {
		apps, err := apollo.Load(st.Target.AppsFile)
		if err != nil {
			st.ApolloError = err.Error()
		} else {
			existing = apps.SteamEntries()
			st.AppsCount = apps.Count()
		}
	}

	root, err := steam.Root()
	if err != nil {
		st.SteamError = err.Error()
		writeJSON(w, st)
		return
	}
	st.SteamRoot = root
	for _, lib := range steam.Libraries(root) {
		st.Libraries = append(st.Libraries, filepath.Dir(lib)) // strip \steamapps
	}
	games, err := steam.InstalledGames(root)
	if err != nil {
		st.SteamError = err.Error()
	}
	for _, g := range games {
		name, in := existing[g.AppID]
		st.Games = append(st.Games, gameView{
			Game:       g,
			InApollo:   in,
			ApolloName: name,
			HasCover:   steam.LocalCover(root, g.AppID) != "",
		})
	}
	writeJSON(w, st)
}

func (s *server) handleCover(w http.ResponseWriter, r *http.Request) {
	root, err := steam.Root()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p := steam.LocalCover(root, r.PathValue("appid"))
	if p == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "max-age=3600")
	http.ServeFile(w, r, p)
}

type applyRequest struct {
	Target  string   `json:"target"`
	Add     []string `json:"add"`
	Remove  []string `json:"remove"`
	Restart bool     `json:"restart"`
}

type applyResult struct {
	AppID  string `json:"appid"`
	Name   string `json:"name"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type applyResponse struct {
	Results      []applyResult `json:"results"`
	Error        string        `json:"error,omitempty"`
	Restarted    bool          `json:"restarted"`
	RestartError string        `json:"restartError,omitempty"`
}

func (s *server) handleApply(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var req applyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := applyResponse{Results: []applyResult{}}
	fail := func(err error) {
		resp.Error = err.Error()
		writeJSON(w, resp)
	}

	installs, err := apollo.Detect()
	if err != nil {
		fail(err)
		return
	}
	in := target(installs, req.Target)
	root, err := steam.Root()
	if err != nil {
		fail(err)
		return
	}
	games, err := steam.InstalledGames(root)
	if err != nil {
		fail(err)
		return
	}
	byID := map[string]steam.Game{}
	for _, g := range games {
		byID[g.AppID] = g
	}
	apps, err := apollo.Load(in.AppsFile)
	if err != nil {
		fail(err)
		return
	}

	changed := false
	for _, id := range req.Remove {
		n, images := apps.RemoveSteamGame(id)
		res := applyResult{AppID: id, Name: byID[id].Name, Action: "remove", OK: n > 0}
		if n == 0 {
			res.Detail = "not in app list"
		}
		// Only delete cover files this tool created.
		for _, img := range images {
			if strings.EqualFold(filepath.Dir(img), in.CoversDir()) {
				_ = os.Remove(img)
			}
		}
		changed = changed || n > 0
		resp.Results = append(resp.Results, res)
	}
	for _, id := range req.Add {
		g, ok := byID[id]
		if !ok {
			resp.Results = append(resp.Results, applyResult{AppID: id, Action: "add", Detail: "not installed"})
			continue
		}
		res := applyResult{AppID: id, Name: g.Name, Action: "add"}
		img, cerr := exportCover(root, in.CoversDir(), id)
		if cerr != nil {
			res.Detail = "added without cover: " + cerr.Error()
		}
		if apps.AddSteamGame(id, g.Name, img) {
			res.OK = true
			changed = true
		} else {
			res.Detail = "already in app list"
		}
		resp.Results = append(resp.Results, res)
	}

	if changed {
		if err := apps.Save(); err != nil {
			if os.IsPermission(err) {
				err = fmt.Errorf("%w (run as administrator)", err)
			}
			fail(err)
			return
		}
		if req.Restart && in.Service != "" {
			if err := apollo.RestartService(in.Service); err != nil {
				resp.RestartError = err.Error()
			} else {
				resp.Restarted = true
			}
		}
	}
	writeJSON(w, resp)
}

// exportCover writes the game's artwork as PNG (Apollo only accepts PNG) and
// returns its absolute path.
func exportCover(steamRoot, dir, appID string) (string, error) {
	var data []byte
	if p := steam.LocalCover(steamRoot, appID); p != "" {
		b, err := os.ReadFile(p)
		if err == nil {
			data = b
		}
	}
	if data == nil {
		b, err := steam.DownloadCover(appID)
		if err != nil {
			return "", fmt.Errorf("no artwork found (%v)", err)
		}
		data = b
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decode artwork: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "steam_"+appID+".png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
