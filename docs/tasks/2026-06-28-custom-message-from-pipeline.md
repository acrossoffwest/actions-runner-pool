# Follow-up: custom message from a pipeline → folded into the run notification

**Status:** idea / not started · **Logged:** 2026-06-28

## What

Let a workflow contribute a free-text message that gets **folded into the same
Telegram notification** sent when its `workflow_run` completes. Example: the
last job computes a deploy summary, and the run-completion alert carries it:

```
✅ deploy passed — acrossoffwest/app
fix: bump to v1.2.3
run #42 · main · push · @acrossoffwest
📝 deployed v1.2.3 to prod        ← text produced by a job
https://github.com/.../runs/42
```

## Why this is non-trivial

The `workflow_run` webhook payload does **not** carry arbitrary text produced by
job steps. So the job-produced message needs a side channel from the workflow to
gharp — and we want that **without a per-repo secret** (the user explicitly
rejected putting a notify token in repo secrets).

## Proposed approach — artifact convention (no secret)

1. In the workflow: write the message to a file and upload it as an artifact
   with a reserved name, e.g.
   ```yaml
   - run: echo "deployed v1.2.3 to prod" > note.txt
   - uses: actions/upload-artifact@v4
     with: { name: gharp-notify, path: note.txt }
   ```
2. In gharp, on `workflow_run` `completed`: use the instance's **existing GitHub
   App installation token** to list the run's artifacts
   (`GET /repos/{repo}/actions/runs/{run_id}/artifacts`). If an artifact named
   `gharp-notify` exists, download + unzip it, read the text (cap length), and
   append it to the notification body built by `buildRunMessage`.
3. Auth is implicit: gharp only reads artifacts from repos where its App is
   installed = the tenant's own repos. No new secret, nothing secret in the
   workflow.

## Open questions / details to settle when picking this up

- Length cap + sanitisation of the artifact text (it lands in a Telegram message).
- Artifact download is a zip; needs unzip in gharp (stdlib `archive/zip`).
- Latency: the artifact must be uploaded before the run completes (it is, since
  upload happens in a job and `workflow_run` fires after all jobs finish).
- Alternative considered: send immediately from the runner (it sits inside the
  tenant's slot, so no GitHub secret needed) — rejected for now because it
  produces a *separate* message rather than folding into the run-completion one.

## Related

Builds on the enrichment work (commit message / workflow name / branch / event /
actor already in `buildRunMessage`). This adds the job-authored line on top.
