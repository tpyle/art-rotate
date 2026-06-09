# art-rotate

Tools for rotating JFrog Artifactory access and identity tokens.

This repo ships three components, all backed by the same Go rotation core:

| Component | What it does |
|---|---|
| `art-rotate` (CLI) | One-shot tool that issues a new token with identical permissions to the one you pass in. |
| `art-k8s-rotate` (Kubernetes) | Controller — runs as a CronJob — that rotates tokens stored inside `kubernetes.io/dockerconfigjson` secrets. |
| `actions/rotate` (GitHub Action) | Composite action that rotates a token inside a workflow and emits the new value as a step output. |

Both JWT access tokens and reference-style identity tokens are supported. When the source token's scope is `applied-permissions/user`, the new token is requested with `include_reference_token=true` so the opaque (Docker-login-compatible) form is in the response.

All three components share `--min-age` and `--expires-within` rotation gates (or `min-age` / `expires-within` inputs for the action). Rotation runs only when the existing token is older than `--min-age` **or** has less than `--expires-within` of life remaining — letting you make scheduled rotation idempotent.

## Quick start

### CLI

Grab a binary from the [latest release](https://github.com/tpyle/art-rotate/releases/latest), then:

```bash
ARTIFACTORY_URL=https://acme.jfrog.io \
ARTIFACTORY_TOKEN=$OLD_TOKEN \
  art-rotate --output stdout
```

Stdout gets the new-token JSON payload; stderr gets logs. Full flag reference: [CLI wiki page](https://github.com/tpyle/art-rotate/wiki/CLI).

### Kubernetes

Each release publishes a `FROM scratch` image to `ghcr.io/tpyle/art-k8s-rotate:<tag>`. Deploy it as a CronJob with `secrets get/list/update` RBAC; full manifest in [examples/cronjob.yaml](examples/cronjob.yaml).

```yaml
args:
  - --namespaces=team-a,team-b
  - --label-selector=art-rotate=true
  - --min-age=168h
```

The controller probes each registry hostname's parent domains to find the Artifactory instance, so a `docker.artifactory.example.com` credential will be rotated against `artifactory.example.com`. Details: [Kubernetes wiki page](https://github.com/tpyle/art-rotate/wiki/Kubernetes).

### GitHub Actions

```yaml
- id: rotate
  uses: tpyle/art-rotate/actions/rotate@v0.3.0
  with:
    artifactory-url: https://acme.jfrog.io
    artifactory-token: ${{ secrets.ARTIFACTORY_TOKEN }}

- env:
    GH_TOKEN: ${{ secrets.ROTATE_GH_TOKEN }}
    NEW_TOKEN: ${{ steps.rotate.outputs.new-token }}
  run: printf '%s' "$NEW_TOKEN" | gh secret set ARTIFACTORY_TOKEN
```

Working templates for both PAT and GitHub App auth are in [examples/](examples/). Details: [GitHub Action wiki page](https://github.com/tpyle/art-rotate/wiki/GitHub-Action).

## Layout

```
cmd/art-rotate/         CLI entrypoint
cmd/art-k8s-rotate/     Kubernetes controller entrypoint
internal/artifactory/   Artifactory Access API client
internal/rotate/        Rotation orchestration (introspect → refresh|create → revoke)
internal/sink/          Output sinks (stdout, file)
internal/discover/      Parent-domain probing for the k8s controller
internal/dockercfg/     dockerconfigjson parser
internal/k8srotate/     k8s controller runner
actions/rotate/         GitHub composite action
examples/               CronJob + GitHub workflow templates
.github/workflows/      CI, release, wiki publishing
wiki/                   Source of the GitHub Wiki (published by .github/workflows/wiki.yml)
```

## Building

```bash
go build ./cmd/art-rotate
go build ./cmd/art-k8s-rotate
go test ./...
```

The container image is built `FROM scratch` with `CGO_ENABLED=0` and `-tags 'netgo,osusergo'`. The Dockerfile only includes `art-k8s-rotate`; the CLI is shipped as release tarballs.
