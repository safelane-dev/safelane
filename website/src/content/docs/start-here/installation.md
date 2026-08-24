---
title: Installation
description: Install SafeLane and its agent skill.
---

## macOS and Linux

```bash
curl -fsSL https://raw.githubusercontent.com/AndrewMaged814/SafeLane/main/docs/install.sh | sh
```

## Windows PowerShell

```powershell
irm https://raw.githubusercontent.com/AndrewMaged814/SafeLane/main/docs/install.ps1 | iex
```

Restart Claude or Codex. The restart loads the SafeLane skill.

Then verify the installation:

```bash
safelane version
```

## Build from source

SafeLane requires Go 1.26.5 or a later version.

```bash
git clone https://github.com/AndrewMaged814/safelane.git
cd safelane
go build -o ./bin/safelane ./cmd/safelane
./bin/safelane version
go test ./...
```

Next: [Check compatibility](../../reference/compatibility/) and [register an application](../../guides/setting-up/).
