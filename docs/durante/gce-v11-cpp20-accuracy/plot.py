"""Render per-case prediction errors for the two locally fitted latency models."""
import gzip
import json
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib.ticker import FixedLocator, FuncFormatter

root = Path(__file__).resolve().parent
result = json.loads(gzip.decompress((root / "evaluation.json.gz").read_bytes()))
plan = json.loads((root / "analysis-plan.json").read_text())
cases = result["cases"]
models = result["models"]["cpp20_train_only"]
fig, ax = plt.subplots(figsize=(8.4, 7.2))
ax.set_xscale("log")
ax.axvspan(0.8, 1.2, color="#d9efe3", label="Within ±20%")
ax.axvline(1, color="#5f7f6d", linewidth=0.8)
positions, labels = [], []
position = 0
for program in plan["original_holdouts"] + plan["additional_holdouts"]:
    for case_id in sorted(k for k, row in cases.items() if row["program"] == program):
        row = cases[case_id]
        positions.append(position)
        labels.append(case_id.replace("/", "  /  "))
        for key, label, color, offset, marker in (
            ("latency", "Latency", "#305ba2", -0.13, "o"),
            ("latency_memory", "Latency + operand bytes", "#ba4a18", 0.13, "s"),
        ):
            predictions = models[key]["cases"][case_id]["prediction_ns"]
            mid = (sum(predictions) / len(predictions)) / row["native_median_ns"]
            low = min(predictions) / row["native_max_ns"]
            high = max(predictions) / row["native_min_ns"]
            ax.plot([low, high], [position + offset] * 2, color=color, linewidth=1.6)
            ax.scatter(mid, position + offset, s=27, marker=marker, color=color,
                       label=label if position == 0 else None, zorder=3)
        position += 1
    position += 0.55
ax.set_yticks(positions, labels)
ax.invert_yaxis()
ax.set_xlim(0.012, 19)
ax.xaxis.set_major_locator(FixedLocator([0.02, 0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 16]))
ax.xaxis.set_major_formatter(FuncFormatter(lambda value, _: f"{value:g}×"))
ax.grid(axis="x", color="#d1d9e1", linewidth=0.55)
ax.set_axisbelow(True)
ax.spines[["top", "right", "left"]].set_visible(False)
ax.tick_params(axis="y", length=0)
ax.set_xlabel("Predicted time / median native CPU time  (log scale)")
fig.suptitle("Held-out C++20 timing estimates on AMD Milan", x=0.05, ha="left",
             fontsize=16, fontweight="bold", y=0.985)
fig.legend(*ax.get_legend_handles_labels(), loc="upper left", bbox_to_anchor=(0.04, 0.955),
           frameon=False, ncol=3)
fig.text(0.05, 0.015, "Lines span both counter observations and the full range of seven native runs.",
         color="#474f57", fontsize=9)
fig.subplots_adjust(left=0.19, right=0.97, bottom=0.11, top=0.875)
fig.savefig(root / "accuracy.png", dpi=145, facecolor="white")
