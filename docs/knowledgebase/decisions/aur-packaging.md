---
type: Decision
title: AUR packaging for Arch Linux
description: Distribute horde on Arch Linux via the AUR (horde-bin), automated by goreleaser on every release tag push.
tags: [decision, packaging, aur, arch-linux, releases]
timestamp: 2026-07-14T00:00:00Z
---

# Context

horde's primary installation methods are Homebrew (macOS) and direct
binary download (GitHub releases). The other target platform is Arch
Linux, where users expect to install via `pacman`/`yay`. The Arch User
Repository (AUR) is the standard distribution channel for packages not
in the official repos.

# Decision

Distribute horde on Arch Linux via an **AUR `horde-bin`** package — a
binary package that downloads the pre-built tarball from the GitHub
release rather than building from source. This mirrors the Homebrew
formula pattern: goreleaser generates and pushes the PKGBUILD
automatically on every release tag push.

## Why `-bin` not `-git`

A `-bin` package downloads the release tarball — fast, deterministic, no
Go toolchain required on the user's machine. A `-git` package would
build from source on every install, which is slow and requires Go. The
release tarballs already cover linux/amd64 and linux/arm64; the PKGBUILD
selects the right one via `$CARCH`.

## How it works

1. goreleaser's `aurs:` block (in `.goreleaser.yml`) generates a
   `PKGBUILD` from the release tarball URL.
2. On tag push, goreleaser pushes the `PKGBUILD` + `.SRCINFO` to
   `ssh://aur@aur.archlinux.org/horde-bin.git` via SSH.
3. Users install with `yay -S horde-bin` (or `paru -S horde-bin`).
4. No cloning, no `makepkg` — the AUR helper handles it.

## Secrets

The `AUR_KEY` repo secret (an SSH private key with access to the AUR
package) is required. If unset, goreleaser logs a notice and skips the
AUR push — the GitHub release and Homebrew formula still succeed. This
mirrors the `HOMEBREW_TAP_TOKEN` pattern.

## Manual fallback

If the automation fails, the PKGBUILD can be updated manually:

```
git clone ssh://aur@aur.archlinux.org/horde-bin.git
# edit PKGBUILD — update pkgver, sha256sums
makepkg --printsrcinfo > .SRCINFO
git commit -am "horde vX.Y.Z"
git push origin master
```

Note: AUR requires the branch be `master`, not `main`. `makepkg` is not
available on macOS — generate `.SRCINFO` manually or on a Linux machine.

# Consequences

* Arch Linux users install via `yay -S horde-bin` — no manual build.
* The AUR package is updated automatically on every release; no manual
  intervention needed once `AUR_KEY` is set.
* The PKGBUILD sources the release tarball from GitHub, so the GitHub
  release must succeed before the AUR push (goreleaser handles this
  ordering).
* If `AUR_KEY` is unset, the AUR push is skipped but the release
  otherwise succeeds.

# Related

* [Releases](/docs/knowledgebase/concepts/releases.md) — the overall
  release process.
* `.goreleaser.yml` — the `aurs:` block.
* `.github/workflows/release.yml` — passes `AUR_KEY` to goreleaser.
