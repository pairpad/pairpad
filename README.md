# Pairpad

Annotate your codebase. Walk your team through it.

Pairpad is a collaborative code annotation tool for developer onboarding. Leave persistent comments and guided tours anchored to your code, then walk new team members through the codebase in real time — or let them explore at their own pace.

## Quick Start

```bash
# Build (requires Go 1.21+ and Node.js 18+)
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
┌──────────────┐        WebSocket         ┌──────────────┐
│    Daemon    │◄───────────────────────► │    Relay     │
│ (filesystem) │   outbound connection    │  (SQLite)    │
└──────────────┘                          └──────┬───────┘
                                                 │ WebSocket
                                                 ▼
                                          ┌──────────────┐
                                          │   Browser    │
                                          │ (CodeMirror) │
                                          └──────────────┘
```

- **Daemon** — watches your filesystem, serves files over WebSocket. Connects outbound only — no inbound ports, works behind firewalls.
- **Relay** — routes messages between daemons and browsers. Stores annotations (comments, tours) in SQLite per project. Never stores source code.
- **Browser** — CodeMirror 6 editor with syntax highlighting, Lezer-based AST anchoring, and all the collaboration UI.

Annotations are identified by project (git remote URL hash). Two developers on the same repo see the same annotations without sharing files.

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
  -s, --server      Relay URL for connect (default ws://localhost:8080)
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

## Building

```bash
make build      # Build the binary
make local      # Build and run locally
make relay      # Build and run relay only
make connect    # Build and run daemon only
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
