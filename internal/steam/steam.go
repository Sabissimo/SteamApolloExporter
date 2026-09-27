// Package steam discovers installed Steam games and their library artwork.
package steam

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/Sabissimo/SteamApolloExporter/internal/vdf"
)

// Game is an installed Steam app.
type Game struct {
	AppID      string `json:"appid"`
	Name       string `json:"name"`
	InstallDir string `json:"installDir"`
	Library    string `json:"library"`
	SizeBytes  int64  `json:"sizeBytes"`
	LastPlayed int64  `json:"lastPlayed"`
	// Tool is true for runtimes/redistributables that aren't really games.
	Tool bool `json:"tool"`
}

// LaunchURL is the Steam protocol command that starts the game.
func (g Game) LaunchURL() string { return "steam://rungameid/" + g.AppID }

// Well-known non-game app IDs (redistributables, runtimes, SteamVR).
var toolIDs = map[string]bool{
	"228980":  true, // Steamworks Common Redistributables
	"250820":  true, // SteamVR
	"1070560": true, // Steam Linux Runtime
	"1391110": true, // Steam Linux Runtime - Soldier
	"1628350": true, // Steam Linux Runtime - Sniper
	"1493710": true, // Proton Experimental
	"2180100": true, // Proton Hotfix
}

var toolNameRe = regexp.MustCompile(`(?i)^(proton\b|steam linux runtime|steamworks common redistributables)`)

// Root returns the Steam install directory from the registry.
func Root() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		if p, _, err := k.GetStringValue("SteamPath"); err == nil && p != "" {
			return filepath.Clean(p), nil
		}
	}
	for _, p := range []string{`C:\Program Files (x86)\Steam`, `C:\Program Files\Steam`} {
		if _, err := os.Stat(filepath.Join(p, "steamapps")); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Steam installation not found")
}

// Libraries returns every steamapps directory across all Steam library folders.
func Libraries(root string) []string {
	seen := map[string]int{} // lower-cased path -> index in out
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		key := strings.ToLower(p)
		if i, ok := seen[key]; ok {
			out[i] = p // prefer libraryfolders.vdf's casing over the registry's
			return
		}
		if _, err := os.Stat(p); err == nil {
			seen[key] = len(out)
			out = append(out, p)
		}
	}
	add(filepath.Join(root, "steamapps"))

	data, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return out
	}
	doc, err := vdf.Parse(string(data))
	if err != nil {
		return out
	}
	folders := doc.Child("libraryfolders")
	keys := make([]string, 0, len(folders))
	for k := range folders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if f, ok := folders[k].(vdf.Node); ok && f.String("path") != "" {
			add(filepath.Join(f.String("path"), "steamapps"))
		}
	}
	return out
}

// InstalledGames scans all libraries for fully installed app manifests.
func InstalledGames(root string) ([]Game, error) {
	byID := map[string]Game{}
	for _, lib := range Libraries(root) {
		manifests, _ := filepath.Glob(filepath.Join(lib, "appmanifest_*.acf"))
		for _, m := range manifests {
			data, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			doc, err := vdf.Parse(string(data))
			if err != nil {
				continue
			}
			st := doc.Child("AppState")
			id, name := st.String("appid"), st.String("name")
			if id == "" || name == "" {
				continue
			}
			// StateFlags bit 4 = fully installed (may be combined with "update required").
			if flags, err := strconv.Atoi(st.String("StateFlags")); err == nil && flags&4 == 0 {
				continue
			}
			size, _ := strconv.ParseInt(st.String("SizeOnDisk"), 10, 64)
			played, _ := strconv.ParseInt(st.String("LastPlayed"), 10, 64)
			byID[id] = Game{
				AppID:      id,
				Name:       name,
				InstallDir: filepath.Join(lib, "common", st.String("installdir")),
				Library:    filepath.Dir(lib),
				SizeBytes:  size,
				LastPlayed: played,
				Tool:       toolIDs[id] || toolNameRe.MatchString(name),
			}
		}
	}
	if len(byID) == 0 {
		return nil, fmt.Errorf("no installed games found under %s", root)
	}
	games := make([]Game, 0, len(byID))
	for _, g := range byID {
		games = append(games, g)
	}
	sort.Slice(games, func(i, j int) bool {
		return strings.ToLower(games[i].Name) < strings.ToLower(games[j].Name)
	})
	return games, nil
}

// Cover image candidates in preference order: portrait capsule first, then header.
var coverNames = []string{"library_600x900", "library_capsule", "header", "library_header"}

// LocalCover finds the best cached library artwork for appID, or "".
func LocalCover(root, appID string) string {
	cache := filepath.Join(root, "appcache", "librarycache")
	found := map[string]string{}

	// Newer Steam: librarycache/<appid>/[<hash>/]<name>.jpg
	dir := filepath.Join(cache, appID)
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := strings.ToLower(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
		if _, ok := found[base]; !ok && isImage(p) {
			found[base] = p
		}
		return nil
	})
	for _, n := range coverNames {
		if p, ok := found[n]; ok {
			return p
		}
	}
	// Older Steam: librarycache/<appid>_<name>.jpg
	for _, n := range coverNames {
		for _, ext := range []string{".jpg", ".png"} {
			p := filepath.Join(cache, appID+"_"+n+ext)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func isImage(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// DownloadCover fetches the capsule art from Steam's CDN when it isn't cached locally.
func DownloadCover(appID string) ([]byte, error) {
	urls := []string{
		"https://shared.steamstatic.com/store_item_assets/steam/apps/" + appID + "/library_600x900.jpg",
		"https://cdn.cloudflare.steamstatic.com/steam/apps/" + appID + "/library_600x900.jpg",
		"https://shared.steamstatic.com/store_item_assets/steam/apps/" + appID + "/header.jpg",
	}
	var lastErr error
	for _, u := range urls {
		resp, err := httpClient.Get(u)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		resp.Body.Close()
		if err == nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
			return body, nil
		}
		lastErr = fmt.Errorf("%s: %s", u, resp.Status)
	}
	return nil, lastErr
}
