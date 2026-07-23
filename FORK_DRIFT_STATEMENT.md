chore(fork-drift): snapshot velatir-specific functional drift

Reference branch documenting the functional drift that
`velatir/sympozium` carried on top of `sympozium-ai/sympozium@21f9171`
as of commit `129cd92` (last Velatir-only commit before the botched
PR #22 merge).

This branch is NOT meant to be merged into `velatir/main`. It is a
read-only snapshot of what a Velatir operator would need to configure
or pin when re-applying the fork's divergence on top of upstream:

## Functional drift captured here

These 8 files are the fork's *operational* differences from upstream.
They affect CI pipelines, the Helm chart shape, and the agent-runner
runtime — i.e., things you cannot safely skip if you want the
`velatir/sympozium` fork to keep building and dispatching to
`velatir-infrastructure` as it has historically.

| File | Why it's a Velatir-specific functional change |
|---|---|
| `.github/workflows/build.yaml` | Fork CI: pushes images to `ghcr.io/velatir/sympozium` (instead of `sympozium-ai/sympozium`), publishes the Helm chart to GHCR OCI on every main push (VEL-1173), and dispatches to `velatir-infrastructure` for chart-version pin updates |
| `.github/workflows/integration-kind.yaml` | Fork CI integration-kind path that publishes the kind cluster image to the Velatir registry |
| `.github/workflows/release.yaml` | Fork release pipeline that targets the Velatir GHCR org and bumps Velatir's downstream `velatir-infrastructure` repo |
| `charts/sympozium/Chart.lock` | Pins the llmfit-dra subchart to 0.3.0 as built by the fork |
| `charts/sympozium/templates/_helpers.tpl` | Velatir-specific image-tag helper that strips the `v` prefix from fork image tags (VEL-1173) |
| `charts/sympozium/values.yaml` | Velatir-default registry/values (e.g., `ghcr.io/velatir/sympozium`, VEL-1173 chart name) |
| `cmd/agent-runner/prompt_service.go` | VEL-1203 prompt-log cap (per-line byte cap, env-var override) |
| `cmd/agent-runner/prompt_service_logging_test.go` | Tests for VEL-1203 log cap |

## What is NOT on this branch (and why)

- `docs/`, `examples/`, `config/samples/`, `AGENTS.md`, `README.md`
  drift: ~45 files of Velatir-internal framing (VEL-1162 sidecar-driven
  docs, Velatir naming conventions, etc.). Skipped intentionally —
  upstream docs are the source of truth and the fork shouldn't
  accumulate doc-drift that nobody reviews.

- Older versions of files that have been updated upstream: the fork
  hadn't synced from upstream in a while, so a few files here were
  "older versions" rather than fork-only additions. Cherry-picking
  the older versions would regress the fork; not done.

## How to use this snapshot

Treat this branch as a checklist when you next sync the fork from
upstream:

1. After merging the sync, diff this branch against the new
   `velatir/main` tip: `git diff velatir/main..velatir/fork-drift-config-statement`
2. Any file in the diff that's *newer* here than on the new main has
   already been brought forward by the sync — drop it from this
   branch.
3. Any file in the diff that's *older* here than on the new main has
   been updated upstream — DON'T bring it forward; you've already
   regressed in the wrong direction.
4. Any file in the diff that's identical needs no action.
5. The remaining files are the true Velatir-specific drift that needs
   to be re-applied as a separate commit on `velatir/main` (typically
   a single `chore(fork): reapply Velatir drift after upstream sync`
   commit).
