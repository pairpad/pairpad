# ![Pairpad](logo.png)

*Annotate your codebase. Walk your team through it.*

Pairpad is a collaborative code annotation tool for developer onboarding. Leave persistent comments and guided tours anchored to your code, then walk new team members through the codebase in real time — or let them explore at their own pace.

## Quick Start

```bash
# Build (requires Go 1.25+ and Node.js 18+)
make build

# Run everything locally — opens your browser
./bin/pairpad local
```

This starts a relay and daemon in one process, serving the current directory. Your browser opens to the session automatically.

## For Teams

One person runs the relay, everyone else connects:

```bash
# On a shared machine or VPS (the relay)
./bin/pairpad relay

# On each developer's machine (the daemon)
./bin/pairpad connect --server ws://relay-host:8080
```

The daemon prints two URLs:
- **Host URL** — for you (has permissions to guide and manage roles)
- **Collaborator URL** — share with your team (or click **Share** in the topbar)

Sessions persist across restarts — the same URL works all day. Use `--new` to start a fresh session, or `--password` to require a password to join.

## Features

### Comments
Click any line number to leave a comment. Supports threaded replies, resolve/unresolve, delete, and line ranges (select multiple lines first). Comments render **bold**, *italic*, and `inline code`. Anchored to code structure via AST — they follow functions when code changes.

### Tours
Click **Tours** to create guided walkthroughs. A tour is an ordered sequence of steps, each pointing to a file and line range with a title and description. Create, edit, reorder, re-anchor, and delete steps from the UI. Tours are the async onboarding tool — create a "Getting Started" tour, and every new team member follows it.

### Guide Mode
Click **Guide** to control everyone's viewport. Your team sees what you see — file, scroll position, cursor, and selection highlights. Combined with a tour, this is a structured live walkthrough. Followers can break away and snap back with **Follow**. The guide sees who's following.

### Presence
See where everyone is — colored dots in the file tree, cursor positions, selection highlights (Google Docs-style). Click a participant's name to jump to their location.

### Roles
Three roles: **host** (daemon owner), **editor** (can edit and create tours), **commenter** (can comment but not edit — safe default for new hires). Host can right-click participants to change roles. Commenters can request edit access — host approves or denies.

### Search
- **Ctrl+P** — quick file picker with fuzzy search
- **Ctrl+Shift+F** — project-wide grep (server-side, respects gitignore)

### Other
- Light/dark theme toggle
- Breadcrumb path bar
- Unsaved file indicators (dot on tab, italic in tree)
- Auto-reconnect with session persistence
- Password-protected sessions
- Structured relay logging for analytics

## How It Works

```
┌───────────────┐                          ┌───────────────┐
│               │   WebSocket (outbound)   │               │
│    Daemon     │─────────────────────────►│    Relay      │
│               │  encrypted file content  │               │
│  • watches    │  HMAC path tokens        │  • routes     │
│    filesystem │  project_connect         │    messages   │
│  • encrypts   │◄─────────────────────────│  • stores     │
│    with       │  request_file            │    annotations│
│    AES-GCM    │  write_file              │    (SQLite)   │
│  • HMAC       │  search_request          │  • manages    │
│    file paths │  activity                │    sessions   │
│               │                          │  • never sees │
└───────────────┘                          │    plaintext  │
                                           └──────┬────────┘
                                                  │
                                                  │ WebSocket
                                                  │ encrypted content
                                                  │ HMAC tokens
                                                  │
                                           ┌──────▼────────┐
                                           │               │
                                           │   Browser     │
                                           │               │
                                           │  • decrypts   │
                                           │    with       │
                                           │    Web Crypto │
                                           │  • CodeMirror │
                                           │    6 editor   │
                                           │  • AST-based  │
                                           │    anchoring  │
                                           └───────────────┘
```

- **Daemon** — runs on the developer's machine, watches the filesystem (`fsnotify`), encrypts all file content and paths before sending. Connects outbound only — no inbound ports needed, works behind firewalls. Auto-reconnects on disconnect.
- **Relay** — routes messages between daemons and browsers. Stores annotations (comments, tours) in SQLite per project. Never sees plaintext source code or real file paths — only ciphertext and HMAC tokens pass through.
- **Browser** — CodeMirror 6 editor with syntax highlighting, Lezer-based AST anchoring, and all the collaboration UI. Decrypts file content client-side using Web Crypto API.

Annotations are identified by project (SHA256 of git remote URL). Two developers on the same repo share annotations without sharing files.

### End-to-End Encryption

The relay is a zero-knowledge message router — it never sees your source code or file paths.

**Key derivation:** When a session is created, the daemon generates a random 8-byte seed and stores it locally in `~/.config/pairpad/sessions/`. Two keys are derived from this seed using HKDF-SHA256:
- **Encryption key** — AES-256-GCM for file content and annotation text
- **HMAC key** — HMAC-SHA256 for file path tokens

**What gets encrypted:**
- All file content (reads, writes, changes) is AES-GCM encrypted by the daemon before sending
- File paths are replaced with deterministic HMAC tokens — the relay routes by token without knowing the real path
- Display paths and search results are also encrypted so the relay cannot read filenames or code snippets
- Comment bodies, tour titles, tour descriptions, and all anchor text/context are encrypted by the browser before sending

**Key distribution:** The encryption seed is appended to the session URL as a fragment (`#sessionId,seed`). URL fragments are never sent to the server — the browser reads the seed from the fragment and derives the same keys using Web Crypto API. Anyone with the URL can decrypt; anyone without it (including the relay operator) cannot.

**What the relay _can_ see:** participant names, session metadata, and message timing/structure. It cannot see file content, file paths, comment text, tour content, or search results.

## CLI Reference

```
Usage: pairpad <command> [flags]

Commands:
  local       Run everything locally (zero-config, opens browser)
  connect     Connect to a remote relay
  relay       Run the relay server
  version     Print version

Common flags:
  -a, --addr        Listen address (default :8080)
  -d, --dir         Project directory (default .)
  -s, --server      Relay URL for connect (default wss://app.pairpad.dev)
  -p, --password    Require password to join session
      --new         Start a new session (default: continue previous)
      --session     Resume a specific session ID
      --no-browser  Don't open browser (local mode)

Environment:
  PAIRPAD_ADDR        Listen address
  PAIRPAD_SERVER      Relay URL
  PAIRPAD_PUBLIC_URL  Public URL for session links
  DATABASE_PATH       SQLite database path
```

## Self-Hosting

Run the relay on any server:

```bash
# Build
make build

# Run (systemd, screen, tmux, etc.)
./bin/pairpad relay --addr :443 --public-url https://your-domain.com
```

The relay handles TLS via autocert (Let's Encrypt) when listening on :443. Use `setcap cap_net_bind_service` to bind to privileged ports without root.

Data is stored in `~/.local/share/pairpad/pairpad.db` (SQLite).

## Install

Download the latest binary from the [releases page](https://github.com/pairpad/pairpad/releases/latest), or build from source:

```bash
# With Go installed
go install github.com/pairpad/pairpad/cmd/pairpad@latest

# Or clone and build
git clone https://github.com/pairpad/pairpad.git
cd pairpad
make build
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
