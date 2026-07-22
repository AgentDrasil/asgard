# WebUI Agent Guidelines

This directory contains the Vue 3 frontend for Asgard.

## Tech Stack & Tools
- **Framework**: Vue 3 (Composition API with `<script setup lang="ts">`)
- **Build Tool**: Vite
- **Styling**: Tailwind CSS v4 + DaisyUI v5
- **Linting & Formatting**: `oxlint` for linting and `oxfmt` for formatting

## Development Commands
When working in `webui/`, use the following npm scripts:
- `npm run dev`: Start Vite development server
- `npm run build`: Type-check and build for production
- `npm run lint`: Run code linter (`oxlint`)
- `npm run lint:fix`: Automatically fix lint issues
- `npm run fmt`: Format code with `oxfmt`
- `npm run fmt:check`: Check code formatting

## Guidelines
1. Run `npm run lint` and `npm run fmt:check` (or `npm run build`) to verify changes before submitting.
2. Use standard Vue 3 Composition API `<script setup lang="ts">` patterns.
3. Keep styling consistent with DaisyUI themes and Tailwind CSS conventions.
4. Prefer Iconify icons (`@iconify/vue`) instead of raw inline SVGs for icons in webpage.
