# Workspace Rules & Agent Guidelines

Welcome to the Asgard workspace! Before writing code or modifying features in this repository, please review the comprehensive documentation in the main project README.

## Frontend Development
When working on frontend code, refer to [webui/AGENTS.md](webui/AGENTS.md) for web UI guidelines, tech stack standards, and npm commands.

## Backend Development
The [README.md](README.md) contains critical documentation regarding:
1. **System Architecture**: Go orchestration layer, CLI-based agents, and Docker runtimes.
2. **Sandbox Architecture**: The dual-sandbox execution environment, Bubblewrap (`bwrap`) parameters, credential masking, and the `fakebash`/`fakebashd` gRPC socketpair communication protocol.
3. **API Endpoints**: The Agent-to-Agent (A2A) protocol implementation, dynamic reload APIs, and team routing structures.

Please refer to [README.md](README.md) for all architectural definitions and security constraints before making any codebase edits.
