# Sandboxes

Every agent has its own sandbox: a container with a persistent home directory at `/home/agent`.
The server drives sandboxes through a small driver interface (`internal/sandbox.Driver`).

## Lifecycle

| State | Meaning |
| --- | --- |
| `creating` | Image pull, network, volume and container are being set up. |
| `starting` / `stopping` | The sandbox is being started or put to sleep. |
| `running` | The sandbox is up. |
| `sleeping` | Stopped. The disk is kept; running processes are not. |
| `rebuilding` | The container is recreated from the current image and resources; the home volume is kept. |
| `destroying` | The sandbox, its home volume and its network are removed, then the agent. |
| `error` | The last operation failed. The message says why; start or rebuild to retry. |

State is stored in SQLite. Each operation first moves the state with a guarded update, so concurrent
requests cannot both act, and then runs in the background. On startup the server reconciles the stored
state with what the driver reports, so interrupted operations and changes made outside the platform (for
example `docker stop`) are picked up.

Changing an agent's image or resources marks it as `needsRebuild`; the change applies on the next rebuild.

## Docker driver

For each agent the Docker driver creates:

- a container `<prefix>-<agent id>`, with `--init`, CPU, memory and PID limits and an optional runtime,
- a volume `<prefix>-home-<agent id>` mounted at `/home/agent`,
- an **internal** bridge network `<prefix>-net-<agent id>` with a subnet from `network_pool`. Internal
  networks have no route to the outside. The server's own container is attached to every agent network
  and is the only way out (see the egress proxy, M4).

The server must be able to reach the Docker socket. When it runs in a container it detects its own
container id automatically; set `server_container` if detection does not work.

```yaml
sandbox:
  driver: docker            # or "fake" for development without Docker
  default_image: debian:bookworm-slim
  default_cpus: 2
  default_memory_mb: 4096
  max_cpus: 8
  max_memory_mb: 16384
  pids_limit: 4096
  docker:
    name_prefix: agenthold  # change to run several installations on one host
    network_pool: 10.213.0.0/16
    subnet_bits: 28         # 14 usable addresses per agent
    runtime: ""             # e.g. sysbox-runc or runsc
    server_container: ""    # autodetected
```

Docker-backed tests carry the `docker` build tag: `make test-docker`.
