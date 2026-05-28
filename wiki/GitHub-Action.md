# GitHub Action — `actions/rotate`

A composite action that rotates an Artifactory token and emits the new value as a step output. Storing the new value back into a GitHub secret (or anywhere else) is the calling workflow's job.

This split keeps the action narrow: you handle the `gh secret set` step with whatever credential you choose, and the action stays out of the GitHub-secret-management permissions discussion entirely.

## Inputs

| Input | Required | Default | Purpose |
|---|---|---|---|
| `artifactory-url` | yes | | Artifactory base URL |
| `artifactory-token` | yes | | The token to rotate |
| `refresh-token` | no | | Refresh token; enables refresh flow in auto mode |
| `admin-token` | no | | Bearer for create/revoke when the rotated token lacks permission |
| `method` | no | `auto` | One of `auto`, `refresh`, `create` |
| `revoke-old` | no | `false` | Delete the old token after issuing the new one |
| `include-reference-token` | no | `auto` | One of `auto`, `yes`, `no` |

## Outputs

| Output | Description |
|---|---|
| `new-token` | The new token. Prefers the reference (opaque) form when the source was an identity token; otherwise the JWT access token. Always masked. |
| `new-token-id` | `token_id` of the new token |
| `expires-in` | Lifetime in seconds (0 = non-expiring) |
| `is-identity-token` | `true` when the new token is an identity token |

## Storing the new value

You need a GitHub token with `secrets: write` permission for the scope you're writing to. The default `GITHUB_TOKEN` does **not** have this — that scope is deliberately missing from the workflow token, and no `permissions:` block can grant it. Use either:

* A fine-grained PAT with `Secrets: Read and write` repo permission, or
* A GitHub App installation token (preferred; minted inline via `actions/create-github-app-token@v1`). The App needs `Secrets: Read and write` configured at creation.

The App approach is preferred — the minted token is short-lived (~1h) and scoped to the App's installations, with no individual user account in the loop.

## Examples

### PAT

```yaml
jobs:
  rotate:
    runs-on: ubuntu-latest
    steps:
      - id: rotate
        uses: tpyle/art-rotate/actions/rotate@v0.2.0
        with:
          artifactory-url: https://acme.jfrog.io
          artifactory-token: ${{ secrets.ARTIFACTORY_TOKEN }}

      - env:
          GH_TOKEN: ${{ secrets.ROTATE_GH_TOKEN }}
          NEW_TOKEN: ${{ steps.rotate.outputs.new-token }}
        run: printf '%s' "$NEW_TOKEN" | gh secret set ARTIFACTORY_TOKEN
```

### GitHub App

```yaml
jobs:
  rotate:
    runs-on: ubuntu-latest
    steps:
      - id: app-token
        uses: actions/create-github-app-token@v1
        with:
          app-id: '123456'
          private-key: ${{ secrets.ROTATE_APP_PRIVATE_KEY }}

      - id: rotate
        uses: tpyle/art-rotate/actions/rotate@v0.2.0
        with:
          artifactory-url: https://acme.jfrog.io
          artifactory-token: ${{ secrets.ARTIFACTORY_TOKEN }}

      - env:
          GH_TOKEN: ${{ steps.app-token.outputs.token }}
          NEW_TOKEN: ${{ steps.rotate.outputs.new-token }}
        run: printf '%s' "$NEW_TOKEN" | gh secret set ARTIFACTORY_TOKEN
```

Both full templates (with `workflow_dispatch`, scheduling, etc.) are in [examples/](https://github.com/tpyle/art-rotate/tree/main/examples).

## Security notes

* The new token is `::add-mask::`'d before being written to `$GITHUB_OUTPUT`, so any accidental `echo` of `${{ steps.rotate.outputs.new-token }}` is redacted in workflow logs.
* The example uses `printf | gh secret set` rather than `--body "$VAL"` so the value never reaches a process command line. (`gh secret set` reads from stdin when `--body` is omitted.)
* The action checks out its own source and builds `art-rotate` with Go on every invocation. If rotation frequency is high enough that the build overhead matters, swap the build step for `curl … releases/download/…` against a pinned release.
