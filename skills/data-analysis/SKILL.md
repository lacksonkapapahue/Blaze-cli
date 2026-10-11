---
name: data-analysis
description: Analyse data from CSV, Excel/Google Sheets exports, JSON or SQLite - clean it, answer questions with numbers, find trends and outliers, make charts and a short written summary. Use for "analyse this spreadsheet", sales/expense/traffic numbers, survey results, logs or any table of data.
tags: [data, csv, excel, spreadsheet, python, pandas, charts]
version: 1
---

# Data analysis (CSV and spreadsheets)

Work in code, not in your head: every number you report comes from a script you ran.
Tooling: Python with pandas (+ matplotlib, openpyxl for .xlsx). Check with
`python -c "import pandas, matplotlib"`; if missing, offer `pip install pandas matplotlib openpyxl`.
No Python? DuckDB CLI (`duckdb -c "select … from 'file.csv'"`) or plain SQL on SQLite also work.

## 1. Understand the question and the data

- Restate the question(s) and what decision they feed. Agree on definitions ("revenue" = after refunds? which time zone? which currency?).
- Inspect before analysing:
  ```python
  import pandas as pd
  df = pd.read_csv("data.csv")            # read_excel("f.xlsx", sheet_name=None) for all sheets
  print(df.shape); print(df.dtypes); print(df.head(10)); print(df.isna().sum()); print(df.describe(include="all").T)
  ```
- Check: units, date formats and time zones, duplicates, IDs, encoding (`encoding="utf-8-sig"` / `latin-1`), thousands separators (`thousands=","`), decimal commas.

## 2. Clean (and log every step)

- Parse dates (`pd.to_datetime(..., errors="coerce")`), numbers (`pd.to_numeric`), trim strings, normalise categories (case, spelling).
- Decide what to do with missing values and outliers - and say so; never silently drop rows. Keep a count: rows in, rows dropped and why, rows out.
- Never modify the original file; write `clean.csv` or work in memory.

## 3. Analyse

- Start simple: totals, counts, means/medians, by group (`groupby().agg()`), over time (`resample("M")`), shares (%), period-over-period change.
- Use medians for skewed data (prices, durations); show distributions, not just averages.
- Compare like with like (same period length, same currency, per-user rates).
- Correlation ≠ causation; small samples (n < 30) and big claims need a caveat.
- Pivot tables: `pd.pivot_table(df, index=…, columns=…, values=…, aggfunc="sum", margins=True)`.

## 4. Charts

- One message per chart; title states it ("Sales doubled after the June campaign").
- Line for time, bar for categories (sorted), histogram for distributions, scatter for relationships. No pies with > 4 slices, no 3D.
- Label axes with units, start bars at zero, highlight the point in one accent colour, grey the rest.
  ```python
  import matplotlib; matplotlib.use("Agg"); import matplotlib.pyplot as plt
  ax = monthly.plot(kind="line", figsize=(9,4.5), color="#1f4bd8"); ax.set_title("…"); ax.set_ylabel("ZMW")
  plt.tight_layout(); plt.savefig("chart.png", dpi=150)
  ```
- Open the PNG and look at it before sharing.

## 5. Report

- **Answer first**: the 2-4 numbers that answer the question, in a sentence each.
- A small table of the key figures; the chart(s); caveats (data quality, assumptions).
- Save the script (`analysis.py`) and outputs next to the data so it can be rerun; offer an Excel export (`df.to_excel("summary.xlsx", index=False)`) or a Google Sheet if useful.

## Checklist

- [ ] Row counts reconcile (in → cleaned → analysed).
- [ ] Totals cross-checked a second way (e.g. sum of groups = overall total).
- [ ] Units and currency on every number; dates in the user's time zone.
- [ ] Every number in the answer came from code that ran this session.
