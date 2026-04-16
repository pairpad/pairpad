# Pairpad

Annotate your codebase. Walk your team through it.

Pairpad is a collaborative code annotation tool for developer onboarding. Leave persistent comments and guided tours anchored to your code, then walk new team members through the codebase in real time — or let them explore at their own pace.

## Quick Start

```bash
# Build (requires Go 1.21+ and Node.js)
make build

# Run everything locally — opens your browser
./bin/pairpad local
```

This starts a relay server and daemon in one process, serving the current directory. Your browser opens to the session automatically.

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
- **Collaborator URL** — share with your team

## What You Can Do

### Comments
Click any line number to leave a comment. Comments support threaded replies, resolve/unresolve, and line ranges (select multiple lines first). Comments are anchored to code structure — they follow functions when code changes.

### Tours
Click **Tours** in the toolbar to create guided walkthroughs. A tour is an ordered sequence of steps, each pointing to a file and line range with a title and description. Tours are the async onboarding tool — a senior developer creates a "Getting Started" tour, and every new team member follows it.

### Guide Mode
Click **Guide** to take control of everyone's viewport. Your team sees what you see — when you open a file, scroll, or highlight code, their editors follow. Combined with a tour, this is a structured live walkthrough. Team members can break away to explore on their own and click **Follow** to snap back.

### Roles
Three roles: **host** (you, the daemon owner), **editor** (can edit files and create tours), **commenter** (can comment but not edit — safe default for new hires). Right-click a participant name to change their role.

### Themes
Click the moon/sun icon to toggle between dark and light themes. Each user sees their own theme.

## How It Works

Pairpad has three components:

1. **Daemon** — runs on the developer's machine, watches the filesystem, serves files over WebSocket
2. **Relay** — stateless server that routes messages between daemons and browsers, stores annotations in SQLite
3. **Browser IDE** — CodeMirror 6 editor with syntax highlighting, comment sidebar, tour navigation, guide mode

The relay stores annotation metadata (comments, tours) but never source code. Code transits as ephemeral WebSocket messages. The daemon connects outbound — no inbound ports, works behind firewalls.

Annotations are anchored to code structure using the editor's parse tree. When code changes, annotations follow the enclosing function/class/type instead of drifting with line numbers.

## CLI Reference

```
Usage: pairpad <command> [flags]

Commands:
  local       Run relay + daemon locally (zero-config, opens browser)
  connect     Connect to a remote relay
  relay       Run the relay server
  login       Authenticate (for hosted service, coming soon)
  version     Print version

Flags:
  -a, --addr       Relay listen address (default :8080)
  -s, --server     Relay URL for connect mode (default ws://localhost:8080)
  -d, --dir        Project directory (default: current directory)

Environment Variables:
  PAIRPAD_ADDR        Relay listen address
  PAIRPAD_SERVER      Relay URL for daemon
  PAIRPAD_PUBLIC_URL  Public URL for session links
  DATABASE_PATH       SQLite database path
```

## Building

```bash
# Prerequisites: Go 1.21+, Node.js 18+
make build          # Build the binary
make local          # Build and run locally
make relay          # Build and run relay only
make connect        # Build and run daemon only
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
