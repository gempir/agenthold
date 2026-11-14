package sandboxd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

// Paths the server writes before each start.
const (
	CAFile       = "/etc/agenthold/ca.pem"
	CABundleFile = "/etc/agenthold/ca-bundle.pem"
)

const caMarker = "# agenthold egress CA"

// installCA adds the egress CA to the image's system trust store, so tools
// that ignore SSL_CERT_FILE still trust intercepted connections. It is best
// effort: the environment variables cover most tools anyway.
func (d *Daemon) installCA() {
	ca, err := os.ReadFile(CAFile)
	if err != nil || os.Geteuid() != 0 {
		return
	}
	// Debian, Ubuntu, Alpine.
	if fi, err := os.Stat("/usr/local/share/ca-certificates"); err == nil && fi.IsDir() {
		dst := "/usr/local/share/ca-certificates/agenthold.crt"
		if cur, err := os.ReadFile(dst); err != nil || !bytes.Equal(cur, ca) {
			if err := os.WriteFile(dst, ca, 0o644); err == nil {
				if path, err := exec.LookPath("update-ca-certificates"); err == nil {
					if out, err := exec.CommandContext(context.Background(), path).CombinedOutput(); err != nil {
						d.log.Warn("update-ca-certificates", "err", err, "output", string(out))
					}
					return
				}
			}
		}
	}
	// Fedora, RHEL.
	if fi, err := os.Stat("/etc/pki/ca-trust/source/anchors"); err == nil && fi.IsDir() {
		if err := os.WriteFile("/etc/pki/ca-trust/source/anchors/agenthold.pem", ca, 0o644); err == nil {
			if path, err := exec.LookPath("update-ca-trust"); err == nil {
				_ = exec.CommandContext(context.Background(), path).Run()
				return
			}
		}
	}
	// Fallback: append to the common bundle once.
	for _, bundle := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/cert.pem", "/etc/pki/tls/certs/ca-bundle.crt"} {
		cur, err := os.ReadFile(bundle)
		if err != nil {
			continue
		}
		if bytes.Contains(cur, ca) {
			return
		}
		f, err := os.OpenFile(bundle, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			continue
		}
		_, _ = f.WriteString("\n" + caMarker + "\n" + string(ca))
		_ = f.Close()
		return
	}
}
