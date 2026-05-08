# Asgard - AI Coding Orchestrator

An AI-powered coding assistant that runs inside Docker, uses Telegram as the frontend interface, and orchestrates CLI-based coding agents (such as gemini-cli) to handle programming tasks.

## Overview

Asgard is designed to be a self-hosted AI coding solution that:
- Runs entirely in Docker for easy deployment and isolation
- Accepts coding requests via Telegram bot interface
- Executes code generation/editing tasks using CLI-based AI agents like `gemini-cli`
- Provides a simple, accessible way to get AI coding assistance

## Quick Start

### Telegram Setup

See [docs/telegram-setup.md](docs/telegram-setup.md) for setup instructions.

## Architecture

- **Frontend**: Telegram Bot API
- **Backend**: Go orchestration layer
- **AI Engine**: CLI-based coding agents (gemini-cli/...)
- **Runtime**: Docker container

## GitHub Account Setup

**Recommended**: Create a separate GitHub account for Asgard to avoid exposing your main account's credentials. If your main account is used and its private tokens leak, an attacker could access all your repositories.

**Alternative**: If you prefer not to set up a separate account, ensure all repositories have:
- Main branch protection enabled
- Require pull requests for all changes

This prevents direct pushes and forces code review even if credentials are compromised.

## License

Apache 2.0
