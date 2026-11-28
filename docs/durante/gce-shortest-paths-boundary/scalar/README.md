# A single shortest-paths coefficient cannot reproduce these native verdicts

The sealed v12 and v13 clocks use a single coefficient in nanoseconds per unit of work. Here one unit is a retired instruction or one reported MOVS/STOS byte. The native results place incompatible bounds on this coefficient, even when it is specific to the original shortest-paths input.

| Input and solver | Native outcome | Completed work across each engine's two boots | Necessary ns per unit |
| --- | --- | ---: | ---: |
| Original graph, block-minimum Dijkstra (width 8), 65 ms | 6/6 AC per engine | 414,483,622 | ≤ 0.15682164 |
| Same graph, reference heap Dijkstra, 12 ms | 6/6 TLE | 63,315,373–63,315,399 | > 0.18952735 |
| Independent graph B, Dial, 30 ms | 6/6 TLE | 137,456,705–137,456,731 | > 0.21825050 |

A TLE requires the unrounded product of the coefficient and work to **exceed** the budget. An AC permits equality. For each pair of requirements, the replay gives the scalar every advantage. It divides the timeout budget by the **largest** completed work observed for that solver and the acceptance budget by the **smallest** observed for the accepted solver. Each strict lower bound still exceeds the inclusive upper bound. The JSON stores exact fractions and individual counter observations.

The original reference finished under its separately registered 20 ms limit. Dial and the width-8 submission finished under their 30 s allowances. Within each row, the boundary run and its completed count have identical ELF, input, and expected output. The historical transfer used one native arm shared by v12 and v13. The two studies used four distinct CPU-pinned Milan guests. The evidence constrains the two different native deadlines on the original input and a third deadline on another input. At the single 65 ms deadline, the twelve-variant panel alone allowed a nonempty interval. No fitted replacement clock was run.

Run `python3 -B witness.py` to reproduce [the results](results.json). It first runs the full [upstream transfer replay](../../gce-v13-judge-transfer/README.md) and [65 ms replay](../README.md) against the published raw receipts, then selects and matches the rows. Run `python3 -B -m unittest -v test_witness` for the strict-bound and provenance tests.
