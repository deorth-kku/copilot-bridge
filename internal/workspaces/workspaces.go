// Package workspaces reads the list of workspaces VS Code knows about from
// the user's globalStorage storage.json and launches them through the
// `code` CLI. storage.json is read-only for this package.
package workspaces

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
)

// Workspace is one entry of VS Code's known-workspaces list.
type Workspace struct {
	// URI is the workspace URI exactly as stored in storage.json
	// (percent-encoding preserved); it is what the CLI receives.
	URI string `json:"uri"`
	// Name is the display name (local: folder name, remote: host:path).
	Name string `json:"name"`
	// Path is the native local path (local workspaces only).
	Path string `json:"path,omitempty"`
	// Remote is the remote authority (remote workspaces only),
	// e.g. "ssh-remote+pve".
	Remote string `json:"remote,omitempty"`
	// Open reports whether a window for this workspace is currently open.
	Open bool `json:"open,omitempty"`
	// LastActive reports whether this is the last active workspace.
	LastActive bool `json:"lastActive,omitempty"`
}

// storage is the subset of storage.json that List reads.
type storage struct {
	ProfileAssociations struct {
		// Workspaces maps workspace URI -> profile id; the keys are the
		// complete known-workspaces list.
		Workspaces map[string]string `json:"workspaces"`
	} `json:"profileAssociations"`
	WindowsState struct {
		OpenedWindows []struct {
			Folder string `json:"folder"`
		} `json:"openedWindows"`
		LastActiveWindow struct {
			Folder string `json:"folder"`
		} `json:"lastActiveWindow"`
	} `json:"windowsState"`
}

// List reads storagePath and returns every known workspace: currently open
// ones first, then local before remote, each group sorted
// case-insensitively by name.
func List(storagePath string) ([]Workspace, error) {
	data, err := os.ReadFile(storagePath)
	if err != nil {
		return nil, err
	}
	var st storage
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", storagePath, err)
	}
	open := make(map[string]bool, len(st.WindowsState.OpenedWindows))
	for _, w := range st.WindowsState.OpenedWindows {
		open[w.Folder] = true
	}
	last := st.WindowsState.LastActiveWindow.Folder

	out := make([]Workspace, 0, len(st.ProfileAssociations.Workspaces))
	for uri := range st.ProfileAssociations.Workspaces {
		ws, ok := parseWorkspace(uri)
		if !ok {
			continue
		}
		ws.Open = open[uri]
		ws.LastActive = uri == last
		out = append(out, ws)
	}
	sortWorkspaces(out)
	return out, nil
}

// Overlay applies the live open state to the known-workspace list, so the
// result follows the actual windows instead of storage.json's
// windowsState (which lags: VS Code does not always rewrite it when a
// window opens or closes). live maps window id -> workspace URI (the
// storage.json key format, as reported by cdp.WorkspaceURI); Open is true
// exactly for the workspaces some live window reports, and live
// workspaces missing from the known list are appended, so a freshly
// opened workspace shows up before storage.json catches up. The result
// is sorted like List's output.
func Overlay(known []Workspace, live map[string]string) []Workspace {
	idx := make(map[string]int, len(known)+len(live))
	out := make([]Workspace, len(known))
	for i, ws := range known {
		ws.Open = false // the live probe is the source of truth
		out[i] = ws
		idx[strings.ToLower(ws.URI)] = i
	}
	for _, uri := range live {
		k := strings.ToLower(uri)
		if i, ok := idx[k]; ok {
			out[i].Open = true
			continue
		}
		ws, ok := parseWorkspace(uri)
		if !ok {
			continue
		}
		ws.Open = true
		idx[k] = len(out)
		out = append(out, ws)
	}
	sortWorkspaces(out)
	return out
}

// sortWorkspaces orders a workspace list: open workspaces first, then
// local before remote, each group sorted case-insensitively by name.
func sortWorkspaces(out []Workspace) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Open != out[j].Open {
			return out[i].Open
		}
		if (out[i].Remote == "") != (out[j].Remote == "") {
			return out[i].Remote == ""
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
}

// normalizeAuthority percent-decodes the authority portion of a URI before
// parsing: storage.json stores remote authorities with '+' encoded as %2B
// (e.g. ssh-remote%2Bpve), which net/url rejects in the host.
func normalizeAuthority(uri string) string {
	i := strings.Index(uri, "://")
	if i < 0 {
		return uri
	}
	rest := uri[i+3:]
	j := strings.IndexByte(rest, '/')
	var auth, tail string
	if j < 0 {
		auth, tail = rest, ""
	} else {
		auth, tail = rest[:j], rest[j:]
	}
	if strings.Contains(auth, "%") {
		if dec, ok := percentDecode(auth); ok {
			auth = dec
		}
	}
	return uri[:i+3] + auth + tail
}

// percentDecode decodes only %XX sequences, leaving every other character
// (notably a literal '+') untouched — unlike url.PathUnescape, which also
// maps '+' to a space and would silently corrupt an authority that stores
// an unencoded '+'. ok is false when s holds a malformed % sequence (the
// caller then keeps the original).
func percentDecode(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
			return "", false
		}
		b.WriteByte(hexVal(s[i+1])<<4 | hexVal(s[i+2]))
		i += 2
	}
	return b.String(), true
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// parseWorkspace turns one storage.json workspace URI into a Workspace.
// ok is false for schemes the CLI cannot open (anything but file:// and
// vscode-remote://).
func parseWorkspace(uri string) (Workspace, bool) {
	u, err := url.Parse(normalizeAuthority(uri))
	if err != nil || u.Scheme == "" {
		return Workspace{}, false
	}
	switch u.Scheme {
	case "file":
		native := nativeFilePath(u)
		name := filepath.Base(native)
		if name == string(filepath.Separator) {
			// Drive root (e.g. E:\): Base returns the separator alone.
			name = native
		}
		return Workspace{
			URI:  uri,
			Name: name,
			Path: native,
		}, true
	case "vscode-remote":
		host := strings.TrimPrefix(u.Host, "ssh-remote+")
		return Workspace{
			URI:    uri,
			Name:   host + ":" + u.Path,
			Remote: u.Host,
			Path:   u.Path,
		}, true
	}
	return Workspace{}, false
}

// nativeFilePath converts a file:// URL to a native filesystem path
// (e.g. /c:/Users/x on Windows -> C:\Users\x; file://server/share ->
// \\server\share).
func nativeFilePath(u *url.URL) string {
	if u.Host != "" {
		return "\\" + u.Host + strings.ReplaceAll(u.Path, "/", "\\")
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		drive := strings.ToUpper(p[1:3])
		rest := strings.ReplaceAll(p[3:], "/", "\\")
		if rest == "" {
			return drive + "\\"
		}
		return drive + rest
	}
	return p
}

// LaunchArgs returns the `code` CLI arguments that open the workspace:
// --folder-uri with the ORIGINAL storage.json URI, for both local and
// remote workspaces (the CLI's URI parser decodes the percent-encoding).
func LaunchArgs(uri string) ([]string, error) {
	u, err := url.Parse(normalizeAuthority(uri))
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", uri, err)
	}
	if u.Scheme != "file" && u.Scheme != "vscode-remote" {
		return nil, fmt.Errorf("unsupported workspace scheme %q", u.Scheme)
	}
	return []string{"--folder-uri", uri}, nil
}

// SameURI reports whether two workspace URI strings refer to the same
// workspace: a storage.json key and the URI a live window reports for
// itself (see cdp.WorkspaceURI). Both use the same format, so an exact
// match is expected; the case-insensitive fallback covers Windows drive
// letters.
func SameURI(storageURI, windowURI string) bool {
	if storageURI == windowURI {
		return true
	}
	return strings.EqualFold(storageURI, windowURI)
}

// Opener launches workspaces through the `code` CLI.
type Opener struct {
	cli string
	log *slog.Logger
}

// NewOpener resolves the code CLI: explicit path > "code" on PATH > the
// standard Windows install location.
func NewOpener(explicit string, log *slog.Logger) *Opener {
	cli := explicit
	if cli == "" {
		if p, err := exec.LookPath("code"); err == nil {
			cli = p
		} else if runtime.GOOS == "windows" {
			if home, err := os.UserHomeDir(); err == nil {
				cli = filepath.Join(home, "AppData", "Local", "Programs", "Microsoft VS Code", "code.exe")
			}
		}
	}
	return &Opener{cli: cli, log: log}
}

// Open launches a VS Code window for the workspace URI. Fire-and-forget:
// the CLI process is started and not waited on (VS Code is a GUI app).
func (o *Opener) Open(uri string) error {
	args, err := LaunchArgs(uri)
	if err != nil {
		return err
	}
	if o.cli == "" {
		return errors.New("code CLI not found (use -code to set its path)")
	}
	o.log.Debug("workspaces: open", "uri", uri, "cli", o.cli)
	cmd := exec.Command(o.cli, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", o.cli, err)
	}
	return nil
}
