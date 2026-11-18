# Optional Strix pull-request security scanning

The [Strix GitHub Actions integration](https://docs.strix.ai/integrations/github-actions)
is available as an **opt-in** scan in
[`.github/workflows/strix-security.yml`](../.github/workflows/strix-security.yml).
It uses the self-hosted Strix CLI to scan pull-request changes for security
problems. The default GitHub Actions security checks remain independent of
Strix, and the controller never needs Strix at runtime.

## Before enabling

1. Review Strix's [upstream project](https://github.com/usestrix/strix),
   [Apache-2.0 license](https://github.com/usestrix/strix/blob/v1.7.1/LICENSE),
   Docker sandbox behaviour, and the model provider's data-handling policy.
   The tool can execute security tests in a container and can send source-code
   context to the chosen external LLM provider. Never use production credentials
   or a live production service as a scan target.
2. In **Repository Settings → Secrets and variables → Actions → Secrets**,
   configure:
   - `STRIX_LLM`: the provider-qualified model name supported by Strix,
     such as `openrouter/z-ai/glm-5.3`.
   - `LLM_API_KEY`: the matching LLM provider API key. Use a separate,
     limited-budget key; never commit it.
3. After reviewing the security and billing implications, add an **Actions
   variable** named `STRIX_ENABLED` with the exact value `true`. Until this
   is set, the Strix job is skipped. Set it to `false` or delete it to disable
   future automatic scans.
4. Open a **same-repository pull request** to verify the check. Forked PRs are
   intentionally skipped so their untrusted code cannot access the LLM secret.

## Behaviour and limits

- The workflow grants only `contents: read` to `GITHUB_TOKEN`, checks out
  full Git history without persisting the token, and runs Strix only against
  repository source in the runner. It does not connect to Vultr, Wasabi, or
  game-server addresses.
- Strix CLI `v1.7.1` is downloaded from a named release and its Linux x86-64
  archive is SHA-256 verified against the digest published by GitHub Releases.
  Do not replace this with an unpinned `curl | bash` installer. Review and
  update the version, archive digest, and notice together.
- The scan uses `quick` mode, the PR's base branch for diff scope,
  `--max-budget 10` (a maximum model spend in the provider's billing
  currency, normally USD), and `--fail-on high`. High and critical validated
  findings fail the check; lower severities can still be reported. Installation
  and configuration errors also fail the check.
- A completion check rejects missing or stopped `run.json` results. Strix
  can still stop early during its own wrap-up while reporting `completed`,
  particularly near the budget cap. Review coverage and raise the budget for
  large changes; this is not a proof of full security coverage.
- Findings are left in the runner and GitHub Actions logs; the workflow does
  not automatically publish sensitive vulnerability details as PR comments,
  downloadable artifacts, or SARIF. **Logs in public repositories can be
  visible publicly**, so review the tool's output before enabling this for
  security-sensitive work. Report actionable vulnerabilities privately per
  [SECURITY.md](../SECURITY.md).
- GitHub-hosted Ubuntu runners need a working Docker daemon. Strix may pull
  sandbox images at runtime; the reviewed CLI release does not pin that
  image's digest. Reassess this additional supply-chain dependency before
  relying on the result as a required merge gate.
- The scanner is optional and not a replacement for unit, integration,
  conformance, access-control, backup/restore, or failure-recovery testing.
  It never authorizes destructive infrastructure operations.

If a scan is skipped, check `STRIX_ENABLED` and whether the pull request
originates inside this repository. If it fails to authenticate, check both
secrets. If it stops on the budget cap, use the run output to decide whether
to raise `--max-budget`. Do not treat a skipped or incomplete scan as a
successful security assessment.
