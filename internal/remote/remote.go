// Package remote mirrors agent session directories from SSH-reachable
// machines into a local cache, so the regular sources can parse them as if
// they were local. Transport is plain `ssh <host> tar` — nothing has to be
// installed on the remote side.
package remote

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/tape/internal/core/model"
	"github.com/chenhg5/tape/internal/core/ports"
)

// AgentDirs are the session directories mirrored from the remote $HOME.
// Keep in sync with the source packages.
var AgentDirs = []string{
	".claude/projects",
	".codex/sessions",
	".cursor/chats",
}

// Mirror pulls session files from one remote host into Dir.
type Mirror struct {
	Host string // ssh destination: host, user@host, or an ssh_config alias
	Dir  string // local mirror root; remote $HOME layout is preserved
}

// HostDir returns the mirror directory for host under tapeDir.
func HostDir(tapeDir, host string) string {
	return filepath.Join(tapeDir, "remotes", sanitizeHost(host))
}

// sanitizeHost turns "user@build-01:2222" into a safe directory name.
func sanitizeHost(host string) string {
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

type state struct {
	LastSync time.Time `json:"last_sync"`
}

func (m *Mirror) statePath() string { return filepath.Join(m.Dir, ".tape-remote.json") }

// Pull fetches new/changed session files from the remote. The first pull is
// full; later pulls only transfer files modified since the last one (with
// one hour of slack — the archive's checksums make re-transfers harmless).
func (m *Mirror) Pull(ctx context.Context) error {
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	var st state
	if data, err := os.ReadFile(m.statePath()); err == nil {
		json.Unmarshal(data, &st)
	}

	newer := ""
	if !st.LastSync.IsZero() {
		// GNU tar and bsdtar both accept this date format
		newer = st.LastSync.Add(-time.Hour).UTC().Format("2006-01-02 15:04:05 UTC")
	}
	started := time.Now().UTC()

	err := m.pullTar(ctx, newer)
	if err != nil && newer != "" {
		// older tar may not support --newer-mtime: retry a full pull
		err = m.pullTar(ctx, "")
	}
	if err != nil {
		return err
	}

	data, _ := json.Marshal(state{LastSync: started})
	return os.WriteFile(m.statePath(), data, 0o600)
}

// remoteScript builds the shell command executed on the remote host: tar up
// whichever agent directories exist there. No agent data is not an error.
func remoteScript(newer string) string {
	var checks []string
	for _, d := range AgentDirs {
		checks = append(checks, fmt.Sprintf(`[ -d "%s" ] && dirs="$dirs %s"`, d, d))
	}
	newerFlag := ""
	if newer != "" {
		newerFlag = fmt.Sprintf(`--newer-mtime "%s" `, newer)
	}
	return fmt.Sprintf(
		`cd "$HOME" && dirs=""; %s; if [ -n "$dirs" ]; then tar -cf - %s$dirs; fi`,
		strings.Join(checks, "; "), newerFlag)
}

func (m *Mirror) pullTar(ctx context.Context, newer string) error {
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", m.Host, remoteScript(newer))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ssh %s: %w", m.Host, err)
	}
	extractErr := extract(stdout, m.Dir)
	waitErr := cmd.Wait()
	if waitErr != nil {
		return fmt.Errorf("ssh %s: %w: %s", m.Host, waitErr, firstLine(errBuf.String()))
	}
	return extractErr
}

// extract unpacks the tar stream under root, refusing entries that escape it.
func extract(r io.Reader, root string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(hdr.Name))
		rel, err := filepath.Rel(root, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("tar entry %q escapes the mirror dir", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			// symlinks etc. are skipped: session stores contain only files
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// WrapSource marks every session loaded from a mirrored source with the
// host it came from (session meta "host", title suffix in summaries comes
// for free via meta). IDs stay globally unique because session uuids are.
func WrapSource(src ports.Source, host string) ports.Source {
	return &hostSource{Source: src, host: host}
}

type hostSource struct {
	ports.Source
	host string
}

// Host implements service.Hosted so sync reports show the origin machine.
func (h *hostSource) Host() string { return h.host }

func (h *hostSource) Load(ctx context.Context, ref ports.SessionRef) (*model.Session, error) {
	s, err := h.Source.Load(ctx, ref)
	if err != nil || s == nil {
		return s, err
	}
	if s.Meta == nil {
		s.Meta = map[string]string{}
	}
	s.Meta["host"] = h.host
	return s, nil
}
