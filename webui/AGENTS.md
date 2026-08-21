# WebUI Agent Guidelines

This directory contains the Vue 3 frontend for Asgard.

## Tech Stack & Tools

- **Framework**: Vue 3 (Composition API with `<script setup lang="ts">`)
- **Build Tool**: Vite
- **Styling**: Tailwind CSS v4 + DaisyUI v5
- **Linting & Formatting**: `oxlint` for linting and `oxfmt` for formatting

## Development Commands

When working in `webui/`, use the following pnpm scripts:

- `pnpm run dev`: Start Vite development server
- `pnpm run build`: Type-check and build for production
- `pnpm run lint`: Run code linter (`oxlint`)
- `pnpm run lint:fix`: Automatically fix lint issues
- `pnpm run fmt`: Format code with `oxfmt`
- `pnpm run fmt:check`: Check code formatting

## Guidelines

1. Run `pnpm run lint` and `pnpm run fmt:check` (or `pnpm run build`) to verify changes before submitting.
2. Use standard Vue 3 Composition API `<script setup lang="ts">` patterns.
3. Keep styling consistent with DaisyUI themes and Tailwind CSS conventions.
4. Prefer Iconify icons (`@iconify/vue`) instead of raw inline SVGs for icons in webpage.
