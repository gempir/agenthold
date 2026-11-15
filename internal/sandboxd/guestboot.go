package sandboxd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// GuestBootVersion is the format version of GuestBoot.
const GuestBootVersion = 1

// GuestBootMaxSize bounds the boot configuration disk that sandboxd reads.
const GuestBootMaxSize = 16 << 20

// GuestBootAlign is the size the encoded configuration is zero-padded to a
// multiple of (block devices have whole sectors; Firecracker wants whole 512
// byte sectors, 4 KiB keeps page-sized reads).
const GuestBootAlign = 4096

// GuestBoot is the boot configuration of a microVM. The host writes it,
// encoded with EncodeGuestBoot, to a small file attached as the read-only
// config drive; sandboxd reads it as PID 1 before anything else.
//
// On the wire it is one JSON object followed by zero bytes up to a multiple of
// GuestBootAlign. Files are base64 in JSON (Go's []byte encoding).
type GuestBoot struct {
	// Version must be GuestBootVersion.
	Version int    `json:"version"`
	AgentID string `json:"agentId"`
	Token   string `json:"token"`
	// TunnelURL is the WebSocket URL sandboxd dials. Empty means
	// ws://<gateway>:<TunnelPort>/tunnel, where the gateway comes from the
	// kernel ip= argument.
	TunnelURL  string `json:"tunnelUrl,omitempty"`
	TunnelPort int    `json:"tunnelPort,omitempty"`
	// Hostname of the guest. Empty means "agent".
	Hostname string `json:"hostname,omitempty"`
	// Env is added to the environment of every process sandboxd starts (it
	// wins over the image's /etc/agenthold/image.env). The ServerPlaceholder
	// "{{server}}" in values is replaced by the gateway address.
	Env map[string]string `json:"env,omitempty"`
	// Files are written into the guest's root (through the overlay) before
	// sandboxd starts, mode 0644, parent directories 0755. Paths are absolute.
	Files map[string][]byte `json:"files,omitempty"`
}

// serverPlaceholder mirrors sandbox.ServerPlaceholder (that package imports
// this one through the fake driver, so it cannot be imported here).
const serverPlaceholder = "{{server}}"

// EncodeGuestBoot validates b and encodes it for the config drive.
func EncodeGuestBoot(b GuestBoot) ([]byte, error) {
	if b.Version == 0 {
		b.Version = GuestBootVersion
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if len(data) > GuestBootMaxSize {
		return nil, fmt.Errorf("boot configuration is %d bytes, more than %d", len(data), GuestBootMaxSize)
	}
	padded := make([]byte, (len(data)+GuestBootAlign-1)/GuestBootAlign*GuestBootAlign)
	copy(padded, data)
	return padded, nil
}

// DecodeGuestBoot reads a configuration written by EncodeGuestBoot (for
// example straight from the config block device).
func DecodeGuestBoot(r io.Reader) (GuestBoot, error) {
	var b GuestBoot
	data, err := io.ReadAll(io.LimitReader(r, GuestBootMaxSize+1))
	if err != nil {
		return b, err
	}
	if len(data) > GuestBootMaxSize {
		return b, errors.New("boot configuration is too large")
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		if len(bytes.Trim(data[i:], "\x00")) != 0 {
			return b, errors.New("boot configuration has nonzero padding")
		}
		data = data[:i]
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return b, errors.New("boot configuration is empty")
	}
	if len(data) > GuestBootMaxSize {
		return b, errors.New("boot configuration is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("parse boot configuration: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return b, errors.New("boot configuration has trailing data")
	}
	if b.Version != GuestBootVersion {
		return b, fmt.Errorf("boot configuration version %d, want %d", b.Version, GuestBootVersion)
	}
	return b, b.validate()
}

func (b GuestBoot) validate() error {
	if b.AgentID == "" || b.Token == "" {
		return errors.New("boot configuration: agentId and token are required")
	}
	if b.TunnelURL == "" && (b.TunnelPort <= 0 || b.TunnelPort > 65535) {
		return errors.New("boot configuration: tunnelUrl or a valid tunnelPort is required")
	}
	if b.Hostname != "" && !validHostname(b.Hostname) {
		return fmt.Errorf("boot configuration: invalid hostname %q", b.Hostname)
	}
	for k, v := range b.Env {
		if strings.ContainsRune(v, '\x00') {
			return fmt.Errorf("boot configuration: environment variable %q contains NUL", k)
		}
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("boot configuration: invalid environment variable %q", k)
		}
	}
	for p := range b.Files {
		if err := checkGuestPath(p); err != nil {
			return fmt.Errorf("boot configuration: file %q: %w", p, err)
		}
	}
	return nil
}

func checkGuestPath(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return errors.New("must be a clean absolute path")
	}
	for _, deny := range []string{"/proc", "/sys", "/dev"} {
		if p == deny || strings.HasPrefix(p, deny+"/") {
			return fmt.Errorf("must not be under %s", deny)
		}
	}
	return nil
}

func validHostname(h string) bool {
	if len(h) > 63 {
		return false
	}
	for i, c := range h {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' && i > 0 && i < len(h)-1
		if !ok {
			return false
		}
	}
	return h != ""
}

// Resolve turns the configuration into sandboxd's per-boot settings for a
// guest whose gateway (the host side of its /30) is gw.
func (b GuestBoot) Resolve(gw net.IP) (Boot, map[string]string, error) {
	host := ""
	if gw != nil {
		host = gw.String()
	}
	url := b.TunnelURL
	if url == "" {
		if host == "" {
			return Boot{}, nil, errors.New("no tunnelUrl and no gateway to derive it from")
		}
		url = "ws://" + net.JoinHostPort(host, strconv.Itoa(b.TunnelPort)) + "/tunnel"
	}
	env := make(map[string]string, len(b.Env))
	for k, v := range b.Env {
		if host != "" {
			v = strings.ReplaceAll(v, serverPlaceholder, host)
		}
		env[k] = v
	}
	if host != "" {
		url = strings.ReplaceAll(url, serverPlaceholder, host)
	}
	return Boot{AgentID: b.AgentID, Token: b.Token, TunnelURL: url}, env, nil
}

// ParseEnvFile parses KEY=VALUE lines (no quoting, no expansion; blank lines
// and lines starting with # are skipped), the format of
// /etc/agenthold/image.env.
func ParseEnvFile(r io.Reader) (map[string]string, error) {
	env := map[string]string{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", n)
		}
		env[k] = v
	}
	return env, sc.Err()
}
