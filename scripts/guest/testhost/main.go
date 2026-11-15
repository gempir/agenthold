// Command testhost stands in for the agenthold node during scripts/guest/boot-test.sh: it serves
// the real tunnel endpoint (internal/tunnel's Hub, same handshake and authentication as the
// server), waits for the guest's sandboxd, runs commands in the guest through the tunnel, can ask
// the guest to shut down, and prints a JSON report. It can also forward DNS for the guest.
//
// Linux or macOS; boot-test.sh runs it inside the Lima VM.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/tunnel"
)

type result struct {
	Cmd    string `json:"cmd"`
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Error  string `json:"error,omitempty"`
	TookMs int64  `json:"tookMs"`
}

type report struct {
	Connected     bool          `json:"connected"`
	ConnectedAtMs int64         `json:"connectedAtMs"` // unix milliseconds
	Hello         *tunnel.Hello `json:"hello,omitempty"`
	Rejected      int           `json:"rejected"` // connection attempts with wrong credentials
	Results       []result      `json:"results"`
	ShutdownAck   bool          `json:"shutdownAck"`
	ShutdownAtMs  int64         `json:"shutdownRequestedAtMs,omitempty"`
	DisconnectMs  int64         `json:"disconnectedAtMs,omitempty"`
	Error         string        `json:"error,omitempty"`
}

type cmdList []string

func (c *cmdList) String() string     { return strings.Join(*c, "; ") }
func (c *cmdList) Set(v string) error { *c = append(*c, v); return nil }

func main() {
	listen := flag.String("listen", "", "tunnel listen address (host:port)")
	agent := flag.String("agent", "ag_boottest", "agent id the guest must present")
	token := flag.String("token", "", "token the guest must present")
	timeout := flag.Duration("timeout", 90*time.Second, "how long to wait for the guest")
	cmdTimeout := flag.Duration("cmd-timeout", 10*time.Minute, "timeout per command")
	shutdown := flag.Bool("shutdown", false, "ask the guest to power off after the commands")
	dns := flag.String("dns", "", "also forward DNS (UDP) on this address to the host's resolver")
	out := flag.String("out", "", "write the JSON report here (default stdout)")
	ready := flag.String("ready", "", "create this file once listening")
	var cmds cmdList
	flag.Var(&cmds, "run", "command to run in the guest as the agent user (bash -lc), repeatable")
	writeConfig := flag.String("write-config", "", "only write a boot configuration disk image to this path and exit")
	port := flag.Int("port", 0, "with -write-config: tunnel port")
	hostname := flag.String("hostname", "", "with -write-config: guest hostname")
	var files, env cmdList
	flag.Var(&files, "file", "with -write-config: GUESTPATH=LOCALPATH to write into the guest, repeatable")
	flag.Var(&env, "env", "with -write-config: KEY=VALUE for the guest environment, repeatable")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "testhost")

	if *writeConfig != "" {
		b := sandboxd.GuestBoot{
			AgentID: *agent, Token: *token, TunnelPort: *port, Hostname: *hostname,
			Env: map[string]string{}, Files: map[string][]byte{},
		}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			b.Env[k] = v
		}
		for _, kv := range files {
			guest, local, _ := strings.Cut(kv, "=")
			data, err := os.ReadFile(local)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			b.Files[guest] = data
		}
		data, err := sandboxd.EncodeGuestBoot(b)
		if err == nil {
			err = os.WriteFile(*writeConfig, data, 0o600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	rep := run(log, *listen, *agent, *token, *timeout, *cmdTimeout, cmds, *shutdown, *dns, *ready)
	data, _ := json.MarshalIndent(rep, "", "  ")
	if *out != "" {
		_ = os.WriteFile(*out, data, 0o644)
	} else {
		fmt.Println(string(data))
	}
	if rep.Error != "" {
		os.Exit(1)
	}
}

func run(log *slog.Logger, listen, agent, token string, timeout, cmdTimeout time.Duration, cmds []string, shutdown bool, dns, ready string) (rep report) {
	rep.Results = []result{}
	var rejected atomic.Int32
	defer func() { rep.Rejected = int(rejected.Load()) }()
	hub := tunnel.NewHub(func(_ context.Context, a, t string) error {
		if a != agent || t != token {
			rejected.Add(1)
			return errors.New("bad credentials")
		}
		return nil
	}, log)
	connected := make(chan struct{}, 1)
	disconnected := make(chan struct{}, 1)
	hub.OnChange(func(_ string, up bool) {
		if up {
			select {
			case connected <- struct{}{}:
			default:
			}
		} else {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}
	})
	mux := http.NewServeMux()
	mux.Handle("/tunnel", hub)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", listen)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	if dns != "" {
		stop, err := forwardDNS(dns, log)
		if err != nil {
			rep.Error = "dns: " + err.Error()
			return rep
		}
		defer stop()
	}
	if ready != "" {
		_ = os.WriteFile(ready, nil, 0o644)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sess, err := hub.Wait(ctx, agent)
	if err != nil {
		rep.Error = "guest did not connect: " + err.Error()
		return rep
	}
	rep.Connected = true
	rep.ConnectedAtMs = sess.ConnectedAt.UnixMilli()
	rep.Hello = &sess.Hello
	log.Info("guest connected", "hello", sess.Hello)

	for _, c := range cmds {
		r := runCmd(sess, c, cmdTimeout)
		log.Info("command", "cmd", c, "code", r.Code, "took", time.Duration(r.TookMs)*time.Millisecond)
		rep.Results = append(rep.Results, r)
	}
	if shutdown {
		rep.ShutdownAtMs = time.Now().UnixMilli()
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := sess.Shutdown(sctx)
		scancel()
		if err != nil {
			rep.Error = "shutdown: " + err.Error()
			return rep
		}
		rep.ShutdownAck = true
		select {
		case <-disconnected:
			rep.DisconnectMs = time.Now().UnixMilli()
		case <-time.After(15 * time.Second):
			rep.Error = "guest did not disconnect after shutdown"
		}
	}
	return rep
}

func runCmd(sess *tunnel.Session, c string, timeout time.Duration) (r result) {
	r = result{Cmd: c, Code: -1}
	t := time.Now()
	defer func() { r.TookMs = time.Since(t).Milliseconds() }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var stderr strings.Builder
	p, err := sess.StartHarness(ctx, []string{"bash", "-lc", "exec </dev/null; " + c}, nil, func(line string) {
		if stderr.Len() < 64<<10 {
			stderr.WriteString(line + "\n")
		}
	})
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer p.Close()
	go func() { <-ctx.Done(); _ = p.Close() }()
	stdout, _ := io.ReadAll(io.LimitReader(p.Stdout, 1<<20))
	<-p.Done()
	code, err := p.ExitCode()
	r.Code, r.Stdout, r.Stderr = code, string(stdout), stderr.String()
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// forwardDNS relays UDP DNS queries on addr to the first nameserver of /etc/resolv.conf.
func forwardDNS(addr string, log *slog.Logger) (func(), error) {
	upstream := "127.0.0.53:53"
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "nameserver" {
				upstream = net.JoinHostPort(f[1], "53")
				break
			}
		}
	}
	pc, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		return nil, err
	}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			go func() {
				c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "udp", upstream)
				if err != nil {
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Write(q); err != nil {
					return
				}
				resp := make([]byte, 64<<10)
				m, err := c.Read(resp)
				if err != nil {
					log.Debug("dns", "err", err)
					return
				}
				_, _ = pc.WriteTo(resp[:m], from)
			}()
		}
	}()
	return func() { _ = pc.Close() }, nil
}
