#!/usr/bin/env python3
"""Render a cloudcent estimate JSON document into a Markdown PR comment.

Usage: render_comment.py <estimate.json> [project-path]

Reads the JSON produced by `cloudcent pulumi estimate -o json` (optionally with
an embedded "guardrail" block) and writes a Markdown summary to stdout suitable
for a sticky pull-request comment.
"""
import json
import sys


def money(x):
    try:
        return f"${float(x):,.2f}"
    except (TypeError, ValueError):
        return str(x)


def main():
    path = sys.argv[1]
    project = sys.argv[2] if len(sys.argv) > 2 else "."

    with open(path) as f:
        doc = json.load(f)

    totals = doc.get("totals", {})
    monthly = totals.get("monthly_total", "0")
    guardrail = doc.get("guardrail") or {}
    resources = doc.get("resources", [])

    passed = guardrail.get("passed", True)
    has_thresholds = any(
        guardrail.get(k) is not None for k in ("budget",)
    ) or guardrail.get("breaches")

    if not guardrail:
        badge = "💰 Cost estimate"
    elif passed:
        badge = "✅ Cost guardrail passed"
    else:
        badge = "❌ Cost guardrail failed"

    lines = [
        f"## {badge}",
        "",
        f"**Project:** `{project}`",
        f"**Estimated monthly cost:** **{money(monthly)}**",
    ]

    baseline = guardrail.get("baseline")
    delta = guardrail.get("delta")
    if baseline is not None and delta is not None:
        pct = guardrail.get("delta_pct")
        arrow = "🔺" if delta > 0 else ("🔻" if delta < 0 else "▪️")
        pct_str = f" ({pct:+.1f}%)" if pct is not None else ""
        lines.append(
            f"**Change vs base branch:** {arrow} {money(delta)}{pct_str} "
            f"(base {money(baseline)})"
        )

    budget = guardrail.get("budget")
    if budget is not None:
        lines.append(f"**Budget:** {money(budget)}")

    breaches = guardrail.get("breaches") or []
    if breaches:
        lines += ["", "**Breaches:**"]
        lines += [f"- ⚠️ {b}" for b in breaches]

    # Per-resource cost table.
    rows = []
    for r in resources:
        name = r.get("name", "")
        sub = r.get("sub_label")
        if sub:
            name = f"{name} · {sub}"
        product = r.get("product", "")
        status = r.get("status", "")
        if status:
            cost = status
        elif r.get("is_usage_based"):
            cost = money(r.get("cost_monthly", "0"))
        elif r.get("on_demand_rate"):
            cost = f"{money(r['on_demand_rate'])}/hr"
        else:
            cost = "—"
        rows.append((name, product, cost))

    if rows:
        lines += [
            "",
            "<details><summary>Per-resource breakdown</summary>",
            "",
            "| Resource | Product | Monthly |",
            "| --- | --- | --- |",
        ]
        for name, product, cost in rows:
            lines.append(f"| {name} | {product} | {cost} |")
        lines.append("")
        lines.append("</details>")

    lines += [
        "",
        "<sub>Estimated by [cloudcent](https://github.com/OverloadBlitz/cloudcent-cli) — "
        "no cloud credentials used. Numbers are list-price approximations.</sub>",
    ]

    print("\n".join(lines))


if __name__ == "__main__":
    main()
