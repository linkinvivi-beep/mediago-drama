# MediaLink Media Asset Transaction Hardening Plan

Date: 2026-08-30

Status: required prerequisite before AutoDL Task 9 imports real provider outputs

## Scope

This plan isolates three media-store concerns discovered while adding durable AutoDL image attempts. It must not change character, scene, prop, storyboard, document-link, prompt, provider, or visible generation workflows.

## Task A: Shorten the global claim critical section

- Download remote assets, validate content limits, decode Base64, and prepare managed temporary files outside `generationClaimMu`.
- Hold `generationClaimMu` only for the final dedupe recheck, atomic asset + cleanup-intent first visibility, and in-memory claim registration.
- A canceled request removes only its own prepared temporary file.
- Add deterministic tests where a slow remote URL is pending while another import, cleanup worker, and `Close` proceed; no test may rely on sleep.
- Verify URL and Base64 limits before allocating or reading the full payload.

## Task B: Preserve concurrent dedupe without reusing crash leftovers

- A `cleanup_pending` asset may be reused only when the same process has an active in-memory claim for that exact asset.
- Crash-leftover pending assets without a live claim remain excluded and are handled only by their persistent cleanup intent.
- Under `generationClaimMu`, concurrent task A/B imports of identical content share one asset ID and increment the same claim count.
- Commit/compensate must preserve the shared asset until the last owner resolves.
- Add same-provider and cross-provider barrier tests asserting A/B asset ID equality, not merely that B survives.

## Task C: Use one same-volume trash per managed root

- Global library assets use the global media root trash.
- Each project library uses a private trash directory on that project's own filesystem volume.
- Poster cache uses its own same-volume trash when its root differs.
- Persist and validate root dev/inode plus per-file trash dev/inode; use the already proven fixed-FD `openat`/`renameatx_np`/`unlinkat` operations.
- Never fall back to copy-and-delete on `EXDEV`.
- Add tests with separate mounted-device fakes or an injected `EXDEV`, project-root relocation, crash after stage, restart restore, and cleanup completion.

## Completion gate

- Repository, media, and generation full tests pass.
- Focused race tests pass repeatedly for slow download, shared dedupe, delete/terminal races, `Close`, and `EXDEV`.
- Ordinary `SaveBase64` assets remain outside cleanup ownership.
- No network request, content decode, or large file read occurs while `generationClaimMu` is held.
- AutoDL provider Task 9 may import real outputs only after this plan is approved.
