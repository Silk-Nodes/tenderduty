# Installing

* [Build from source and run under systemd](#build-from-source-and-run-under-systemd)
* [Docker](#docker-container)
* [Docker Compose](#docker-compose)

## Build from source and run under systemd

This is how we run it. Building needs Go 1.26 or later: see [go.dev/doc/install](https://go.dev/doc/install), or on Ubuntu `sudo snap install go --classic`.

Build and install the binary to `~/go/bin`, then set up a working directory for the config and state file:

```shell
git clone https://github.com/Silk-Nodes/tenderduty
cd tenderduty
make install
mkdir -p ~/tenderduty
cp example-config.yml ~/tenderduty/config.yml
# edit ~/tenderduty/config.yml: chains, nodes and notification channels
```

Create the service. The heredoc runs in your shell, so `$USER` and `$HOME` are filled in for whoever runs it, there is no need for a dedicated system user. The isolation settings leave the service with write access to its working directory only, which is where the state file lives.

```shell
sudo tee /etc/systemd/system/tenderduty.service > /dev/null << EOF
[Unit]
Description=Tenderduty
After=network.target
ConditionPathExists=$HOME/go/bin/tenderduty

[Service]
Type=simple
Restart=always
RestartSec=5
TimeoutSec=180

User=$USER
WorkingDirectory=$HOME/tenderduty
ExecStart=$HOME/go/bin/tenderduty

# there may be a large number of network connections if a lot of chains
LimitNOFILE=infinity

# extra process isolation
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$HOME/tenderduty
RestrictSUIDSGID=true
LockPersonality=true
PrivateUsers=true
PrivateDevices=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now tenderduty
sudo journalctl -fu tenderduty
```

A second instance, for a testnet config say, is the same file under a different name, with the config passed with `-f` and its own state file passed with `-state`, since both run in the same directory. Each instance also needs its own `listen_port` and `prometheus_listen_port` in its config.

```shell
sudo tee /etc/systemd/system/tenderduty-testnet.service > /dev/null << EOF
[Unit]
Description=Tenderduty (testnet)
After=network.target
ConditionPathExists=$HOME/go/bin/tenderduty

[Service]
Type=simple
Restart=always
RestartSec=5
TimeoutSec=180

User=$USER
WorkingDirectory=$HOME/tenderduty
ExecStart=$HOME/go/bin/tenderduty -f $HOME/tenderduty/testnet-config.yml -state $HOME/tenderduty/.tenderduty-testnet-state.json

LimitNOFILE=infinity
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$HOME/tenderduty
RestrictSUIDSGID=true
LockPersonality=true
PrivateUsers=true
PrivateDevices=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
```

To upgrade: `git pull && make install && sudo systemctl restart tenderduty`. `tenderduty version` prints the tag, the
commit and the Go toolchain the binary was built with.

`make build` writes the binary to the repo directory instead, and `make docker` builds the container image locally.

## Docker Container

```shell
mkdir tenderduty && cd tenderduty
docker run --rm ghcr.io/silk-nodes/tenderduty:latest -example-config >config.yml
# edit config.yml and add chains, notification methods etc.
docker run -d --name tenderduty -p "8888:8888" -p "28686:28686" --restart unless-stopped -v $(pwd)/config.yml:/var/lib/tenderduty/config.yml ghcr.io/silk-nodes/tenderduty:latest
docker logs -f --tail 20 tenderduty
```

## Docker Compose

```shell
mkdir tenderduty && cd tenderduty

cat > docker-compose.yml << EOF
---
services:

  tenderduty:
    image: ghcr.io/silk-nodes/tenderduty:latest
    command: ""
    ports:
      - "8888:8888" # Dashboard
      - "28686:28686" # Prometheus exporter
    volumes:
      - home:/var/lib/tenderduty
      - ./config.yml:/var/lib/tenderduty/config.yml
    logging:
      driver: "json-file"
      options:
        max-size: "20m"
        max-file: "10"
    restart: unless-stopped

volumes:
  home:
EOF

docker compose pull
docker run --rm ghcr.io/silk-nodes/tenderduty:latest -example-config >config.yml

# Edit the config.yml file, and then start the container
docker compose up -d
docker compose logs -f --tail 20
```
