# CLI — `art-rotate`

A one-shot tool that introspects an existing Artifactory token, issues a new one with the same scope/audience/refreshability, and writes the result out. Designed for cron, systemd timers, and CI pipelines.

## Install

Download a release tarball:

```bash
VERSION=v0.2.0
curl -sSL "https://github.com/tpyle/art-rotate/releases/download/${VERSION}/art-rotate_${VERSION}_linux_amd64.tar.gz" \
  | tar -xz
```

Or from a checkout:

```bash
go build -o art-rotate ./cmd/art-rotate
```

Releases ship `linux`, `darwin`, and `windows` binaries for `amd64` and `arm64`.

## Usage

```text
art-rotate --url URL --token TOKEN [flags]
```

### Required

| Flag | Env | Description |
|---|---|---|
| `--url` | `ARTIFACTORY_URL` | Artifactory base URL, e.g. `https://acme.jfrog.io` |
| `--token` | `ARTIFACTORY_TOKEN` | The token to rotate |

### Rotation behaviour

| Flag | Default | Purpose |
|---|---|---|
| `--method` | `auto` | One of `auto`, `refresh`, `create` |
| `--refresh-token` | env `ARTIFACTORY_REFRESH_TOKEN` | Enables the refresh flow in auto mode |
| `--admin-token` | env `ARTIFACTORY_ADMIN_TOKEN` | Bearer for create/revoke when the rotated token lacks permission |
| `--revoke-old` | `false` | DELETE the old token after the new one is emitted |
| `--include-reference-token` | `auto` | One of `auto`, `yes`, `no`. Auto turns on when source has `applied-permissions/user` scope |
| `--expires-in` | mirrored | Override the new token's TTL in seconds (0 = non-expiring) |
| `--description` | mirrored + suffix | Override the description |

### Output

| Flag | Default | Purpose |
|---|---|---|
| `--output` | `stdout` | Repeatable / comma-separated. One or more of `stdout`, `file` |
| `--output-file` | | Path for the `file` sink (0600, atomic via temp + rename) |

### HTTP

| Flag | Default | Purpose |
|---|---|---|
| `--timeout` | `30s` | HTTP timeout |
| `--insecure-skip-verify` | `false` | Skip TLS verification (dev only; emits a stderr warning) |

Run `art-rotate --help` for the complete list.

## Examples

Rotate an access token and capture the new value:

```bash
ARTIFACTORY_URL=https://acme.jfrog.io \
ARTIFACTORY_TOKEN=$(cat /etc/art/token) \
  art-rotate --output stdout | jq -r .access_token > /etc/art/token.new
```

Rotate via the refresh flow (no admin needed; both tokens must already be paired):

```bash
art-rotate \
  --url https://acme.jfrog.io \
  --token "$ACCESS_TOKEN" \
  --refresh-token "$REFRESH_TOKEN" \
  --method refresh \
  --output file --output-file /etc/art/token.json
```

Rotate an identity token (auto-detected) and pull the opaque form for a Docker login:

```bash
NEW=$(art-rotate --output stdout | jq -r '.reference_token // .access_token')
echo "$NEW" | docker login docker.artifactory.example.com -u alice --password-stdin
```

## Output JSON

Written to stdout (and to the `file` sink if configured):

```json
{
  "access_token": "<JWT>",
  "reference_token": "<opaque>",
  "token_id": "<new id>",
  "expires_in": 3600,
  "scope": "applied-permissions/user",
  "audience": "*@*",
  "refreshable": true,
  "is_identity_token": true
}
```

`reference_token` is only present for identity tokens.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | New token issued and emitted to every sink |
| `1` | Introspection or rotation failed; no new token issued |
| `2` | New token was issued but a sink or revoke step failed; the value is still usable, follow-up may be needed |
