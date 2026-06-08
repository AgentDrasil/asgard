# Status line customization

From: https://antigravity.google/docs/cli-statusline

Toggle standard status line components, define custom scripting configurations, and format dynamic JSON state payloads.

## Overview

The status line is positioned at the bottom of the TUI prompt panel. It provides at-a-glance context regarding active agent cycles, workspace environments, context token window usages, and background execution tasks.

## Interactive toggling

1. Type `/statusline` in the prompt box and press `Enter`.
2. The interactive **Status Picker Panel** overlay opens.
3. Use `↑`/`↓` to toggle specific metric elements (e.g., active model, task counters, context percentage) on and off.
4. Press `Enter` to commit your selection and exit. Press `Esc` to cancel.

## Custom status line scripting

For advanced terminal layouts or custom status bar displays, you can route active agent metadata into a custom script.

### Configuration

Add a `statusLine` configuration block to your `~/.gemini/antigravity-cli/settings.json` file:

content\_copy

```
            {
  "statusLine": {
    "type": "command",
    "command": "~/.gemini/antigravity-cli/statusline.sh"
  }
}

```

Whenever the agent state changes, the TUI executes your command script, pipes a detailed state JSON payload directly to the script's `stdin`, reads your formatted string from `stdout`, and renders the result in the prompt's status line. Full ANSI color codes are supported.

### Available JSON fields

The JSON payload piped to your script contains the following top-level fields:

| Field | Type | Description |
| --- | --- | --- |
| `cwd` | string | Current working directory |
| `conversation_id` | string | Current conversation ID |
| `model` | object | `id` and `display_name` of the active model |
| `product` | string | Application name (e.g., `antigravity-cli`) |
| `workspace` | object | `current_dir` and `project_dir` paths |
| `version` | string | CLI version string |
| `plan_tier` | string | Subscription tier of the authenticated user |
| `email` | string | LDAP/email of the authenticated user |
| `agent` | object | Active agent profile name |
| `context_window` | object | `total_input_tokens`, `total_output_tokens`, `context_window_size`, `used_percentage`, `remaining_percentage`, `current_usage` |
| `agent_state` | string | Current state: `idle`, `thinking`, `working`, `tool_use`, `initializing` |
| `vcs` | object | Version control info: `type` (git/jj/fig), `branch`, `client`, `dirty` |
| `sandbox` | object | Sandbox config: `enabled`, `allow_network` |
| `subagents` | array | Active subagent sessions with `name`, `role`, `status` |
| `artifacts` | array | Produced artifacts with `uri`, `status`, `type` |
| `pending_input_count` | int | Number of queued user messages |
| `background_tasks` | array | Running tasks with `name`, `status`, `index` |
| `tool_confirmation_pending` | bool | Whether a tool confirmation dialog is showing |
| `terminal_width` | int | Live width of the interactive terminal |

### JSON payload example

Here is a fully sanitized, typical JSON payload piped to your status line script:

content\_copy

```
            {
  "cwd": "/home/user/my-project",
  "conversation_id": "12345678-abcd-ef01-2345-6789abcdef01",
  "model": {
    "id": "Gemini",
    "display_name": "Gemini"
  },
  "workspace": {
    "current_dir": "/home/user/my-project",
    "project_dir": "file:///home/user/my-project"
  },
  "version": "2026.04.15",
  "context_window": {
    "total_input_tokens": 88244,
    "total_output_tokens": 61074,
    "context_window_size": 1048576,
    "used_percentage": 8.415603637695312,
    "remaining_percentage": 91.58439636230469,
    "current_usage": {
      "input_tokens": 63382,
      "output_tokens": 346,
      "cache_creation_input_tokens": 0,
      "cache_read_input_tokens": 20857
    }
  },
  "product": "antigravity-cli",
  "agent_state": "idle",
  "vcs": {
    "type": "git",
    "client": "my-project",
    "branch": "dev",
    "dirty": false
  },
  "sandbox": {
    "enabled": false
  },
  "plan_tier": "Pro",
  "email": "developer@email.com",
  "terminal_width": 111
}

```

### Example script

You can download a complete, layout-adaptive script from the official [statusline.sh example on GitHub](https://github.com/google-antigravity/antigravity-cli/blob/main/examples/statusline/statusline.sh). This script renders state badges, handles active branches, and formats context window progress bars dynamically.

Save the script to `~/.gemini/antigravity-cli/statusline.sh` and make it executable:

content\_copy

```
            chmod +x ~/.gemini/antigravity-cli/statusline.sh

```

## See also

- **[Terminal Title Customization](https://antigravity.google/docs/cli-title)**: Configure dynamic window titles.
- **[Settings, Rendering & Keybindings](https://antigravity.google/docs/cli-settings)**: Customize keyboard hotkeys and buffers.
- **[Permissions & Sandbox](https://antigravity.google/docs/cli-sandbox)**: Manage secure directory permissions.
