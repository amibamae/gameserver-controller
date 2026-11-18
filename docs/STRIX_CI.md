# Manual Strix security scanning in GitHub Actions

The optional [Strix CI/CD integration](https://docs.strix.ai/integrations/ci-cd)
is implemented in [`strix-on-demand.yml`](../.github/workflows/strix-on-demand.yml).
It runs the open-source Strix CLI in a GitHub-hosted Ubuntu runner, using a
model provider that the operator chooses.

**This integration has no push, pull-request, timer, or automatic trigger.**
It runs only from a GitHub Actions `workflow_dispatch` event with an explicit
per-run acknowledgement, on `main`. It is not a required merge check, and
ordinary Go, protocol, and plugin validation is unaffected.

## One-time setup

1. Read the [Strix documentation](https://docs.strix.ai/integrations/ci-cd)
   and [Strix v1.7.1 license](https://github.com/usestrix/strix/blob/v1.7.1/LICENSE).
   Strix runs active security tests in a Docker sandbox and sends repository
   code context and prompts to your chosen external AI model provider. Review
   that provider's terms and retention policies before enabling scans.
2. Create a separate, spending-limited API key with an LLM provider that
   Strix supports, such as OpenRouter.
3. Open **Repository → Settings → Secrets and variables → Actions** and add
   the repository secrets:
   - `STRIX_LLM`: the provider-qualified model ID supported by Strix;
     for example `openrouter/z-ai/glm-5.3`.
   - `LLM_API_KEY`: that model provider's API key. Never commit or print it.
4. Open **Repository → Actions → Strix on-demand security scan → Run workflow**.
   Select `main`, check the explicit model-cost approval box, and run it.
   If the box is not checked, or a different branch is selected, the scan
   job is skipped.

The workflow does **not** require `STRIX_ENABLED` and does not run when
a pull request is opened. It does **not** use Strix Cloud credentials.

## Limits and safety

- The scan targets the checked-out repository (`./`) in `quick` mode with
  `--scope-mode full`. It does not target a deployed service and should never
  be supplied production credentials, infrastructure addresses, Minecraft
  data, or player information.
- The Strix model budget is `--max-budget 3` (nominally USD $3 per
  invocation) and the GitHub Actions job has a 45-minute timeout.
  Third-party provider rates and billing rules can differ; this is not a
  monthly spending cap, nor a guaranteed charge.
- Missing secrets, CLI download or checksum errors, execution errors, and
  high/critical validated findings cause the job to fail. Findings below the
  `high` threshold can be present in a successful result.
- The workflow rejects a missing scan report and reports whose
  `run.json` status is not `completed`. Strix can nevertheless finish
  early when nearing the budget limit and still report completion. A green
  run does **not** establish complete test coverage.
- GitHub token privileges are read-only (`contents: read`). The checkout
  does not retain the GitHub token, and the workflow does not publish SARIF,
  automatically post findings to public PRs, or modify the repository.
- GitHub Actions logs may be public. Review all scanner outputs for sensitive
  vulnerability details. Use [private vulnerability
  reporting](../SECURITY.md) for actionable vulnerabilities.
- Strix CLI v1.7.1 is installed from its fixed GitHub release and its
  SHA-256 checksum is validated against release metadata. The first scan
  can also download its sandbox Docker image at runtime, whose tag rather
  than content digest is chosen by Strix. This additional supply-chain
  risk remains and should be reviewed if Strix is later made mandatory.
- The workflow is optional; it is not a substitute for threat modelling,
  regular unit and conformance tests, or careful manual review of
  credential handling, destructive operations, backup safety and
  infrastructure permissions.

To disable future scans, do nothing: there is no automatic trigger.
To prevent *manual* scans, remove the model secrets or the workflow.
