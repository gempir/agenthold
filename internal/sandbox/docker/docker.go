// Package docker is the Docker sandbox driver. It is the only package that
// imports the Docker SDK.
//
// Every agent gets:
//   - a container named <prefix>-<agent id> (the sandbox id),
//   - a volume <prefix>-home-<agent id> mounted at /home/agent,
//   - an internal bridge network <prefix>-net-<agent id> with a subnet carved
//     from the configured pool. The network has no route to the outside; the
//     server's container is attached to it and is the only way out.
package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/gempir/agenthold/internal/sandbox"
)

const (
	labelManaged = "agenthold.managed"
	labelPrefix  = "agenthold.prefix"
	labelAgent   = "agenthold.agent"
	homeDir      = "/home/agent"
)

// Config configures the driver.
type Config struct {
	Host            string
	NamePrefix      string
	NetworkPool     netip.Prefix
	SubnetBits      int
	Runtime         string
	ServerContainer string
	PIDsLimit       int64
	Logger          *slog.Logger
}

// Driver implements sandbox.Driver with Docker.
type Driver struct {
	cfg Config
	cli *client.Client
	log *slog.Logger

	netMu sync.Mutex // serializes subnet allocation
}

var _ sandbox.Driver = (*Driver)(nil)

// New connects to Docker.
func New(ctx context.Context, cfg Config) (*Driver, error) {
	opts := []client.Opt{client.FromEnv}
	if cfg.Host != "" {
		opts = append(opts, client.WithHost(cfg.Host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		return nil, fmt.Errorf("docker is not reachable: %w", err)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ServerContainer == "" {
		cfg.ServerContainer = detectSelf()
	}
	d := &Driver{cfg: cfg, cli: cli, log: cfg.Logger}
	if cfg.ServerContainer != "" {
		d.log.Info("docker driver: attaching server container to agent networks", "container", cfg.ServerContainer)
	} else {
		d.log.Warn("docker driver: server is not running in a container; sandboxes will not reach it")
	}
	return d, nil
}

// detectSelf returns our container id when running inside Docker.
func detectSelf() string {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return ""
	}
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// Client exposes the Docker client to tests in this package tree.
func (d *Driver) Client() *client.Client { return d.cli }

// Name implements sandbox.Driver.
func (d *Driver) Name() string { return "docker" }

func (d *Driver) containerName(agentID string) string { return d.cfg.NamePrefix + "-" + agentID }
func (d *Driver) volumeName(agentID string) string    { return d.cfg.NamePrefix + "-home-" + agentID }
func (d *Driver) networkName(agentID string) string   { return d.cfg.NamePrefix + "-net-" + agentID }

func (d *Driver) labels(agentID string) map[string]string {
	return map[string]string{labelManaged: "true", labelPrefix: d.cfg.NamePrefix, labelAgent: agentID}
}

// Create implements sandbox.Driver.
func (d *Driver) Create(ctx context.Context, spec sandbox.Spec) (sandbox.ID, error) {
	if err := d.ensureImage(ctx, spec.Image); err != nil {
		return "", err
	}
	netName, err := d.ensureNetwork(ctx, spec.AgentID)
	if err != nil {
		return "", err
	}
	if _, err := d.cli.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: d.homeVolume(spec), Labels: d.labels(spec.AgentID),
	}); err != nil {
		return "", fmt.Errorf("create home volume: %w", err)
	}
	if err := d.createContainer(ctx, spec, netName); err != nil {
		return "", err
	}
	return sandbox.ID(d.containerName(spec.AgentID)), nil
}

func (d *Driver) homeVolume(spec sandbox.Spec) string {
	if spec.HomeVolume != "" {
		return spec.HomeVolume
	}
	return d.volumeName(spec.AgentID)
}

func (d *Driver) createContainer(ctx context.Context, spec sandbox.Spec, netName string) error {
	env := make([]string, 0, len(spec.Env)+1)
	env = append(env, "HOME="+homeDir)
	for _, k := range sortedKeys(spec.Env) {
		env = append(env, k+"="+spec.Env[k])
	}
	mounts := []mount.Mount{{Type: mount.TypeVolume, Source: d.homeVolume(spec), Target: homeDir}}
	for _, m := range spec.Mounts {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	pids := spec.Resources.PIDsLimit
	if pids == 0 {
		pids = d.cfg.PIDsLimit
	}
	res := container.Resources{
		NanoCPUs: int64(spec.Resources.CPUs * 1e9),
		Memory:   spec.Resources.MemoryMB * 1024 * 1024,
	}
	if pids > 0 {
		res.PidsLimit = &pids
	}
	init := true
	_, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: d.containerName(spec.AgentID),
		Config: &container.Config{
			Image:      spec.Image,
			Hostname:   "agent",
			Env:        env,
			Entrypoint: spec.Entrypoint,
			Cmd:        spec.Cmd,
			WorkingDir: homeDir,
			Labels:     d.labels(spec.AgentID),
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(netName),
			Mounts:        mounts,
			Runtime:       d.cfg.Runtime,
			Init:          &init,
			Resources:     res,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		},
	})
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

func (d *Driver) ensureImage(ctx context.Context, ref string) error {
	if _, err := d.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image: %w", err)
	}
	d.log.Info("pulling image", "image", ref)
	res, err := d.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer res.Close()
	if err := res.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}

// ensureNetwork creates the agent's internal network (if needed) and returns its name.
func (d *Driver) ensureNetwork(ctx context.Context, agentID string) (string, error) {
	name := d.networkName(agentID)
	d.netMu.Lock()
	defer d.netMu.Unlock()
	if _, err := d.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); err == nil {
		return name, nil
	} else if !errdefs.IsNotFound(err) {
		return "", err
	}
	subnet, err := d.freeSubnet(ctx)
	if err != nil {
		return "", err
	}
	gw := subnet.Addr().Next()
	_, err = d.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver:   "bridge",
		Internal: true,
		Labels:   d.labels(agentID),
		IPAM:     &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet, Gateway: gw}}},
		Options:  map[string]string{"com.docker.network.bridge.enable_icc": "true"},
	})
	if err != nil {
		return "", fmt.Errorf("create network: %w", err)
	}
	return name, nil
}

// freeSubnet returns the first subnet of the pool that overlaps no existing Docker network.
func (d *Driver) freeSubnet(ctx context.Context) (netip.Prefix, error) {
	nets, err := d.cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return netip.Prefix{}, err
	}
	var used []netip.Prefix
	for _, n := range nets.Items {
		for _, c := range n.IPAM.Config {
			if c.Subnet.IsValid() {
				used = append(used, c.Subnet)
			}
		}
	}
	return nextFree(d.cfg.NetworkPool, d.cfg.SubnetBits, used)
}

func nextFree(pool netip.Prefix, bits int, used []netip.Prefix) (netip.Prefix, error) {
	pool = pool.Masked()
	size := uint32(1) << (32 - bits)
	start := ip4(pool.Addr())
	count := uint32(1) << (bits - pool.Bits())
	for i := range count {
		cand := netip.PrefixFrom(fromIP4(start+i*size), bits)
		free := true
		for _, u := range used {
			if cand.Overlaps(u) {
				free = false
				break
			}
		}
		if free {
			return cand, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("network pool %s is exhausted", pool)
}

func ip4(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func fromIP4(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// attachServer connects the server's own container to the agent network.
func (d *Driver) attachServer(ctx context.Context, netName string) error {
	if d.cfg.ServerContainer == "" {
		return nil
	}
	_, err := d.cli.NetworkConnect(ctx, netName, client.NetworkConnectOptions{Container: d.cfg.ServerContainer})
	if err != nil && !isAlreadyConnected(err) {
		return fmt.Errorf("attach server to %s: %w", netName, err)
	}
	return nil
}

func isAlreadyConnected(err error) bool {
	return errdefs.IsConflict(err) || errdefs.IsAlreadyExists(err) ||
		strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already attached")
}

func (d *Driver) agentOf(ctx context.Context, id sandbox.ID) (string, error) {
	res, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return "", sandbox.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return res.Container.Config.Labels[labelAgent], nil
}

// Start implements sandbox.Driver.
func (d *Driver) Start(ctx context.Context, id sandbox.ID) error {
	agentID, err := d.agentOf(ctx, id)
	if err != nil {
		return err
	}
	if err := d.attachServer(ctx, d.networkName(agentID)); err != nil {
		return err
	}
	if _, err := d.cli.ContainerStart(ctx, string(id), client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// Stop implements sandbox.Driver.
func (d *Driver) Stop(ctx context.Context, id sandbox.ID) error {
	timeout := 10
	_, err := d.cli.ContainerStop(ctx, string(id), client.ContainerStopOptions{Timeout: &timeout})
	if errdefs.IsNotFound(err) {
		return sandbox.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("stop container: %w", err)
	}
	return nil
}

// Destroy implements sandbox.Driver.
func (d *Driver) Destroy(ctx context.Context, id sandbox.ID, opts sandbox.DestroyOpts) error {
	agentID := strings.TrimPrefix(string(id), d.cfg.NamePrefix+"-")
	if a, err := d.agentOf(ctx, id); err == nil && a != "" {
		agentID = a
	}
	if _, err := d.cli.ContainerRemove(ctx, string(id), client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove container: %w", err)
	}
	netName := d.networkName(agentID)
	if d.cfg.ServerContainer != "" {
		_, _ = d.cli.NetworkDisconnect(ctx, netName, client.NetworkDisconnectOptions{Container: d.cfg.ServerContainer, Force: true})
	}
	if _, err := d.cli.NetworkRemove(ctx, netName, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove network: %w", err)
	}
	if !opts.KeepHome {
		if _, err := d.cli.VolumeRemove(ctx, d.volumeName(agentID), client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove home volume: %w", err)
		}
	}
	return nil
}

// Status implements sandbox.Driver.
func (d *Driver) Status(ctx context.Context, id sandbox.ID) (sandbox.Status, error) {
	res, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return sandbox.Status{State: sandbox.StateMissing}, nil
	}
	if err != nil {
		return sandbox.Status{}, err
	}
	c := res.Container
	st := sandbox.Status{}
	switch {
	case c.State == nil:
		st.State = sandbox.StateStopped
	case c.State.Running || c.State.Restarting:
		st.State = sandbox.StateRunning
	case c.State.Status == container.StateCreated:
		st.State = sandbox.StateCreated
	default:
		st.State = sandbox.StateStopped
		st.Detail = fmt.Sprintf("exit code %d", c.State.ExitCode)
		if c.State.OOMKilled {
			st.Detail += " (out of memory)"
		}
	}
	if c.NetworkSettings != nil {
		for _, ep := range c.NetworkSettings.Networks {
			if ep != nil && ep.Gateway.IsValid() {
				st.GatewayIP = ep.Gateway.String()
			}
		}
	}
	return st, nil
}

// Rebuild implements sandbox.Driver: the container is replaced, the network
// and the home volume are kept.
func (d *Driver) Rebuild(ctx context.Context, id sandbox.ID, spec sandbox.Spec) error {
	if string(id) != d.containerName(spec.AgentID) {
		return errors.New("rebuild: sandbox id does not belong to this agent")
	}
	if err := d.ensureImage(ctx, spec.Image); err != nil {
		return err
	}
	netName, err := d.ensureNetwork(ctx, spec.AgentID)
	if err != nil {
		return err
	}
	if _, err := d.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: d.homeVolume(spec), Labels: d.labels(spec.AgentID)}); err != nil {
		return fmt.Errorf("ensure home volume: %w", err)
	}
	if _, err := d.cli.ContainerRemove(ctx, string(id), client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove old container: %w", err)
	}
	return d.createContainer(ctx, spec, netName)
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
