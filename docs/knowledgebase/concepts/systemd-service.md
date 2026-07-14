---
type: Concept
title: systemd service
description: Running horde as a user systemd service on Linux — unit file, environment, and management.
tags: [systemd, linux, service, deployment]
timestamp: 2026-07-14T00:00:00Z
---

horde is designed for single-user operation (see the [persistence
decision](/docs/knowledgebase/decisions/persistence-and-knowledgebase.md)):
one user running the node, with data in `~/.local/`. The natural way to
run it as a persistent background process on Linux is a **user systemd
service** — not a system-wide service, which would run as root or a
dedicated system user and conflict with the XDG home-directory layout.

# User service vs system service

| | User service | System service |
| --- | --- | --- |
| Unit location | `~/.config/systemd/user/` | `/etc/systemd/system/` |
| Runs as | the user | root or a system user |
| Data paths | `~/.local/share/horde`, `~/.local/state/horde` | requires custom paths |
| `loginctl enable-linger` | required (to run without a login session) | not needed |
| XDG env vars | resolved from `$HOME` | must be set explicitly |

horde's config and data paths default to the user's home directory. A
user service is the correct fit.

# Setup

## 1. Create the unit file

Create `~/.config/systemd/user/horde.service`:

```ini
[Unit]
Description=horde node — distributed multi-agent system
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/horde serve --mode master
Restart=on-failure
RestartSec=5

# Optional: override config via environment.
# Environment=HORDE_SERVER_PORT=13420
# Environment=HORDE_MODE=master
# Environment=HORDE_LOG_LEVEL=info

[Install]
WantedBy=default.target
```

### Slave mode

For a slave node, change the `ExecStart` line and set the leader address:

```ini
ExecStart=/usr/bin/horde serve --mode slave
Environment=HORDE_SERVER_LEADER=master-host:13420
Environment=HORDE_CLUSTER_NODE_ID=slave-1
```

## 2. Enable lingering (so the service runs without an active login session)

```bash
loginctl enable-linger $USER
```

Without this, the user manager stops when you log out, killing horde.

## 3. Reload and enable

```bash
systemctl --user daemon-reload
systemctl --user enable horde
systemctl --user start horde
```

# Management

```bash
# Check status
systemctl --user status horde

# View logs (journalctl)
journalctl --user -u horde -f

# Restart after config changes
systemctl --user restart horde

# Stop
systemctl --user stop horde

# Disable (stop running at login)
systemctl --user disable horde
```

# Configuration

The service inherits the user's environment. horde's config is loaded
from the [layered config system](configuration.md):

1. `~/.config/horde/horde.yaml` (or `.json` / `.toml`)
2. `HORDE_*` environment variables (set via `Environment=` lines in the
   unit file)

See [environment](environment.md) for the full list of config keys and
env vars. The [XDG data paths](environment.md#data-and-state-directories-xdg)
(`HORDE_PATHS_*`) default to the user's home directory and need no
override for a user service.

# Verifying

After starting the service, verify the node is reachable:

```bash
curl http://localhost:13420/api/v1/health
# {"status":"ok"}

curl http://localhost:13420/api/v1/ready
# {"status":"ready","leader":"ok"}
```

The TUI connects automatically:

```bash
horde
```

# Notes

* The `--daemonize` flag (`horde serve --daemonize`) is an alternative
  that forks into the background without systemd. It is useful for
  ad-hoc runs but does not provide restart-on-failure, logging via
  journald, or lifecycle management. Prefer the systemd service for
  persistent deployment.
* The agent subprocesses (`horde agent`) are spawned by the node
  automatically; they do not need their own unit files. They are
  children of the `horde serve` process and are torn down with it.
* If the binary is installed via AUR (`yay -S horde-bin`), the
  `ExecStart` path is `/usr/bin/horde`. If installed manually, adjust
  the path accordingly.
