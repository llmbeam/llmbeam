# Releasing LLMBeam

LLMBeam uses GoReleaser to publish macOS, Linux, and Windows binaries and to update the Homebrew cask.

## One-time Homebrew setup

1. Create a public repository named `llmbeam/homebrew-tap`.
2. Create a fine-grained GitHub personal access token with `Contents: Read and write` access to that repository.
3. Add the token to this repository as an Actions secret named `HOMEBREW_TAP_GITHUB_TOKEN`.

The release workflow initializes the tap automatically if it is empty.

GoReleaser will create and update `Casks/llmbeam.rb` in the tap repository. Users can then install with:

```sh
brew install llmbeam/tap/llmbeam
```

## Publish a release

Make sure `main` is clean and CI is passing, then create and push a semantic version tag:

```sh
git tag -a v0.1.0 -m "LLMBeam v0.1.0"
git push origin v0.1.0
```

The release workflow publishes:

- macOS amd64 and arm64 tarballs;
- Linux amd64 and arm64 tarballs;
- Windows amd64 and arm64 zip files;
- SHA-256 checksums;
- release notes generated from commits;
- an updated Homebrew cask.

The Linux installer at `install.sh` reads the latest GitHub Release and verifies its archive with `checksums.txt`. Keep the archive naming and checksum output stable when changing the GoReleaser configuration.

Use a new version tag to retry a failed release after fixing its cause. Do not move a tag that users may already have fetched.
