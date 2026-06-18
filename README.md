# Asgard - AI Coding Orchestrator

An AI-powered coding assistant that runs inside Docker and orchestrates CLI-based coding agents (such as gemini-cli) to handle programming tasks.

## Overview

Asgard is designed to be a self-hosted AI coding solution that:
- Runs entirely in Docker for easy deployment and isolation
- Executes code generation/editing tasks using CLI-based AI agents like `gemini-cli`
- Provides a simple, accessible way to get AI coding assistance

## Architecture

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
