export interface A2UIKpi {
  id: string;
  label: string;
  value: number | string;
  format?: "currency" | "percent" | "number" | "string";
  change?: string;
  changeType?: "positive" | "negative" | "neutral";
  subtext?: string;
  icon?: string;
  color?: "primary" | "secondary" | "accent" | "emerald" | "amber" | "purple" | "sky" | "rose";
}

export type A2UIWidget =
  | A2UIChartWidget
  | A2UIDataTableWidget
  | A2UIHoldingsTableWidget
  | A2UIMarkdownWidget
  | A2UIKeyValListWidget;

export interface A2UIBaseWidget {
  id: string;
  title?: string;
  description?: string;
  colSpan?: 1 | 2 | 3 | 4;
}

export interface A2UIChartDataset {
  label?: string;
  data: number[];
  backgroundColor?: string | string[];
  borderColor?: string;
  borderWidth?: number;
  borderDash?: number[];
  borderRadius?: number;
  pointRadius?: number;
  pointHoverRadius?: number;
  [key: string]: any;
}

export interface A2UIChartWidget extends A2UIBaseWidget {
  type: "chart";
  chartType: "donut" | "doughnut" | "pie" | "bar" | "horizontal-bar" | "line";
  labels: string[];
  datasets: A2UIChartDataset[];
  format?: "currency" | "percent" | "number" | "string";
  yAxisFormat?: "currency" | "percent" | "number" | "string";
  options?: Record<string, any>;
}

export interface A2UIDataTableColumn {
  key: string;
  label: string;
  align?: "left" | "center" | "right";
  format?: "currency" | "percent" | "number" | "badge" | "text" | "date";
  badgeColorMap?: Record<string, string>;
  sortable?: boolean;
}

export interface A2UIDataTableWidget extends A2UIBaseWidget {
  type: "data-table";
  sourceCsv?: string;
  headers?: string[];
  rows?: Array<Record<string, any>>;
  columns?: A2UIDataTableColumn[];
  valueField?: string;
  features?: {
    search?: boolean;
    sort?: boolean;
    filters?: Array<{
      key: string;
      label: string;
      options?: string[];
    }>;
    pagination?: boolean;
    exportCsv?: boolean;
    showTotals?: boolean;
  };
}

export interface A2UIHoldingsTableWidget extends A2UIBaseWidget {
  type: "holdings-table";
  sourceCsv?: string;
  headers?: string[];
  rows?: Array<Record<string, any>>;
  columns?: A2UIDataTableColumn[];
  valueField?: string;
  weightField?: string;
  accountField?: string;
  taxField?: string;
  ownerField?: string;
  features?: {
    search?: boolean;
    sort?: boolean;
    aggregateBy?: string;
    filters?: Array<{
      key: string;
      label: string;
      options?: string[];
    }>;
    exportCsv?: boolean;
  };
}

export interface A2UIMarkdownWidget extends A2UIBaseWidget {
  type: "markdown";
  sourceMd?: string;
  content?: string;
}

export interface A2UIKeyValListWidget extends A2UIBaseWidget {
  type: "key-val-list";
  items: Array<{
    label: string;
    value: number | string;
    format?: "currency" | "percent" | "number" | "string";
    subtext?: string;
    color?: string;
    progressPct?: number;
  }>;
}

export interface A2UITab {
  id: string;
  label: string;
  icon?: string;
  layout?: "grid-2" | "grid-3" | "stacked";
  widgets: A2UIWidget[];
}

export interface A2UIManifest {
  schemaVersion?: "1.0" | string;
  title: string;
  asOfDate?: string;
  subtitle?: string;
  kpis?: A2UIKpi[];
  tabs?: A2UITab[];
}
