# kumiho-plugin-metadata-kitsu

Kitsu manga metadata provider for Kumiho.

## Features

- runtime type: `service`
- endpoints: `/search`, `/fetch`, `/health`, `/manifest`
- capabilities: `metadata.search`, `metadata.fetch`
- public Kitsu Edge API search by default
- optional token-backed Algolia search when a Kitsu access token is configured
- fetch returns characters sorted with `main` first, then alphabetical order, capped at 20

## Supported content type

- `comic`

The plugin returns `unsupported` for `book`, `novel`, and `audiobook`.

## Environment variables

- `KUMIHO_PLUGIN_HOST`: bind host
- `KUMIHO_PLUGIN_PORT`: bind port
- `KITSU_ACCESS_TOKEN`: optional user-scoped Kitsu access token
- `KITSU_REFRESH_TOKEN`: optional refresh token used when the access token expires

## Local development

```bash
go test ./...
go run .
```

## Release flow

This repository ships releases through GitHub Actions.

1. Merge changes into `main`
2. Create and push a tag such as `v0.1.0`
3. The `release.yml` workflow builds release binaries for Linux and publishes a GitHub Release

Published asset names follow this pattern:

- `kumiho-plugin-metadata-kitsu_v0.1.0_linux_amd64`
- `kumiho-plugin-metadata-kitsu_v0.1.0_linux_arm64`
- `checksums.txt`

The release workflow also verifies that the Git tag version matches `plugin/plugin.go`.
