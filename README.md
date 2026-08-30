# scanchat — *One binary. Any local LLM. Scan and chat.*

![demo](docs/demo.gif)
<!-- TODO: Record a <=15-second, <=5 MB GIF showing `scanchat` in a terminal, the QR code appearing, a phone scanning it, and a reply streaming in. -->

- No app, no account, and no Docker: run one binary and open the browser already on your phone.
- Works with Ollama, LM Studio, llama.cpp, and any OpenAI-compatible server.
- Uses one-time QR pairing, restart-revocable in-memory sessions, and loopback-only auto-discovered backends.

## Install

The Homebrew tap and install script below are placeholders for the first public release. Until then, use a release archive or `go install`.

### Homebrew

```sh
brew install shao-hua-li/tap/scanchat
```

### Install script

```sh
curl -fsSL https://scanchat.dev/install.sh | sh
```

### Release archives

Download the matching archive from [GitHub Releases](https://github.com/shao-hua-li/scanchat/releases/latest):

| Platform | Architecture | Archive |
| --- | --- | --- |
| macOS | Apple Silicon | `scanchat_VERSION_darwin_arm64.tar.gz` |
| macOS | Intel | `scanchat_VERSION_darwin_amd64.tar.gz` |
| Linux | ARM64 | `scanchat_VERSION_linux_arm64.tar.gz` |
| Linux | x86-64 | `scanchat_VERSION_linux_amd64.tar.gz` |
| Windows | ARM64 | `scanchat_VERSION_windows_arm64.zip` |
| Windows | x86-64 | `scanchat_VERSION_windows_amd64.zip` |

### Go

Requires Go 1.22 or newer:

```sh
go install github.com/shao-hua-li/scanchat@latest
```

## Quick start

```sh
ollama serve
scanchat
# Scan the QR code with your phone.
```

Keep the terminal open. Your phone and computer must be on the same Wi-Fi network. Run `scanchat --help` to see options for the port, pairing-code lifetime, QR output, and additional OpenAI-compatible backends.

## How it works

```text
Phone browser
      | HTTP + SSE over the LAN
      v
scanchat gateway (:8442)
      +-- OpenAI-compatible API --> loopback backends
```

At startup, scanchat probes the standard local endpoints for Ollama, LM Studio, and llama.cpp. On macOS it also probes OMLX. Models are presented under namespaced IDs, and chat responses stream unchanged from the selected backend to the phone. Use `--backend http://127.0.0.1:8000/v1` to add another compatible endpoint.

## Security

- Pairing codes are generated with `crypto/rand`, expire after 10 minutes by default, and rotate immediately after successful use.
- The QR carries the code in the URL fragment, keeping it out of HTTP request logs; successful pairing creates a random 32-byte `HttpOnly`, `SameSite=Lax` session cookie.
- Failed pairing is limited per IP: five failures in one minute trigger a five-minute lockout.
- Auto-discovered upstreams use loopback addresses. Explicit `--backend` URLs are trusted operator input and may point elsewhere.
- Chat, model, and session API routes require authentication; POST requests reject foreign origins, and all responses include restrictive browser security headers.
- Sessions live only in memory and are all revoked when scanchat restarts. Per-device revocation is planned.

scanchat serves plain HTTP because phones need to reach it directly over the LAN. That means traffic and session cookies are not encrypted in transit. Its v1 threat model is a trusted home or office Wi-Fi network: do not expose the port to the public internet, add router port forwarding, or use it on an untrusted network. A future tunnel adapter will provide authenticated TLS for remote access.

## Comparison

These projects solve related problems with different scopes; choose the one that matches your setup.

| | scanchat | Open WebUI | LM Link | Native mobile apps |
| --- | --- | --- | --- | --- |
| Install effort | One static binary | Deploy a web application or container | Included with LM Studio | Install on each phone |
| Works with any backend | OpenAI-compatible APIs | Broad backend support | LM Studio | Varies by app |
| No account required | Yes | Local user setup by default | Yes | Varies by app |
| Phone setup | Scan one QR; use browser | Open URL and sign in | Scan from LM Studio | Install and configure app |

Open WebUI is the stronger fit for a full multi-user interface and persistent history. LM Link is the shortest path for an LM Studio-only workflow. Native apps can offer deeper mobile integration. scanchat focuses on a small, ephemeral bridge from local OpenAI-compatible servers to any phone browser.

## Roadmap

- v1.1 adapters for Tailscale Serve and Cloudflare Quick Tunnels.
- Device management UI with individual session revocation.
- Sidecar and library modes for embedding the gateway in other tools.

## License

[MIT](LICENSE)
