package egress

import (
	"context"
	"os"
	"strconv"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Paths inside the sandbox (written by the server before each start).
const (
	SandboxCAFile     = "/etc/agenthold/ca.pem"
	SandboxBundleFile = "/etc/agenthold/ca-bundle.pem"
)

// SandboxEnv is the environment every sandbox gets: proxy hints and the CA
// bundle for common tools. Interception does not depend on these; they make
// tools that do honor them trust intercepted TLS and use the explicit proxy.
func SandboxEnv(explicitPort string) map[string]string {
	proxy := "http://" + sandbox.ServerPlaceholder + ":" + explicitPort
	noProxy := "localhost,127.0.0.1,::1," + sandbox.ServerPlaceholder
	return map[string]string{
		"HTTPS_PROXY": proxy, "https_proxy": proxy,
		"HTTP_PROXY": proxy, "http_proxy": proxy,
		"NO_PROXY": noProxy, "no_proxy": noProxy,
		"SSL_CERT_FILE":       SandboxBundleFile,
		"REQUESTS_CA_BUNDLE":  SandboxBundleFile,
		"CURL_CA_BUNDLE":      SandboxBundleFile,
		"GIT_SSL_CAINFO":      SandboxBundleFile,
		"AWS_CA_BUNDLE":       SandboxBundleFile,
		"NODE_EXTRA_CA_CERTS": SandboxCAFile,
		"DENO_CERT":           SandboxCAFile,
	}
}

// BootFiles returns the CA certificate and a full bundle (system roots of the
// server plus the agenthold CA) to write into the sandbox.
func (c *CA) BootFiles() map[string][]byte {
	var bundle []byte
	for _, p := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/cert.pem", "/etc/pki/tls/certs/ca-bundle.crt"} {
		if b, err := os.ReadFile(p); err == nil {
			bundle = b
			break
		}
	}
	bundle = append(bundle, '\n')
	bundle = append(bundle, c.certPEM...)
	return map[string][]byte{SandboxCAFile: c.certPEM, SandboxBundleFile: bundle}
}

// SpecFunc builds sandbox specs: the agent's settings, the proxy environment
// and the placeholders of attached secrets.
func SpecFunc(st *store.Store, explicitPort int) sandbox.SpecFunc {
	return func(a db.Agent) sandbox.Spec {
		s := sandbox.BaseSpec(a)
		s.Env = SandboxEnv(strconv.Itoa(explicitPort))
		if rows, err := st.ListAgentSecrets(context.Background(), a.ID); err == nil {
			for _, r := range rows {
				if r.EnvVar != "" {
					s.Env[r.EnvVar] = r.Secret.Placeholder
				}
			}
		}
		return s
	}
}
