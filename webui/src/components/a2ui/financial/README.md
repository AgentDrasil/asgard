# Financial AI Specialized A2UI Components

This directory contains specialized A2UI (Agent-to-UI) components tailored for the **Financial Bot / Financial AI** project:
🔗 **[elmhuangyu/financial-bot](https://github.com/elmhuangyu/financial-bot)**

## Overview

While general-purpose A2UI components (such as KPI grids, charts, generic data tables, and markdown renderers) reside in [`../common`](../common), the components in this directory provide domain-specific visual layouts and data aggregations designed for financial portfolio workflows.

## Components

### `A2UIHoldingsTable.vue`

A comprehensive financial holdings & portfolio allocation component that supports:

- **Asset Allocation & Portfolio Weights**: Calculates and renders percentage of total portfolio value (`pct_of_portfolio`), weights, market values, and asset classes.
- **Aggregation Mode**: Interactive grouping and roll-up view by asset class, account, tax status, or sector.
- **Financial Number Formatting**: High-precision money formatting, percentage deltas with color-coded badges, and formatted share counts.
- **Search & Column Filters**: Real-time filtering by account type, asset category, tax wrapper, and ticker/symbol.
- **CSV Export**: Direct one-click download of the portfolio holding data.

## Related Links

- Upstream / Related Repository: [https://github.com/elmhuangyu/financial-bot](https://github.com/elmhuangyu/financial-bot)
