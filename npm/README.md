# direxio-connect

Bridge local AI coding agents to a Direxio Matrix agents room.

Chat with your AI dev assistant from anywhere.

## Install

```bash
npm install -g direxio-connent
```

## Usage

```bash
# Create config
direxio-connect --version

# Edit config.toml, then run
direxio-connect
direxio-connect -config /path/to/config.toml

# Optional daemon service
direxio-connect daemon install --config /path/to/config.toml --force

# Use one service name per Direxio node on the same machine
direxio-connect daemon install --config /path/to/t1/config.toml --service-name t1.direxio.ai --force
```

## Documentation

See full documentation at: https://github.com/YingSuiAI/direxio-connect
