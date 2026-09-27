// Command SteamApolloExporter is a GUI that exports installed Steam games,
// with their launch commands and cover art, into Apollo's app list.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

//go:embed ui
var uiFiles embed.FS

func init() {
	// WebView2 must be driven from the thread that created the window.
	runtime.LockOSThread()
}

func main() {
	token := randomToken()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("Could not start the local UI server:\n" + err.Error())
	}
	ui, _ := fs.Sub(uiFiles, "ui")
	srv := newServer(ui)
	mux := http.NewServeMux()
	// Every route lives under a random per-run prefix so other local
	// processes and web pages can't drive the API.
	prefix := "/" + token
	mux.Handle(prefix+"/", http.StripPrefix(prefix, srv.routes()))
	go func() { _ = http.Serve(ln, mux) }()

	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "SteamApolloExporter", "webview")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  dataDir,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Steam → Apollo Exporter",
			Width:  1180,
			Height: 800,
			IconId: 1, // icon embedded by go-winres
			Center: true,
		},
	})
	if w == nil {
		fatal("Microsoft Edge WebView2 Runtime is required.\nInstall it from https://go.microsoft.com/fwlink/p/?LinkId=2124703")
	}
	defer w.Destroy()
	w.SetSize(900, 560, webview2.HintMin)
	w.Navigate("http://" + ln.Addr().String() + prefix + "/")
	w.Run()
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func fatal(msg string) {
	title, _ := windows.UTF16PtrFromString("Steam → Apollo Exporter")
	text, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
