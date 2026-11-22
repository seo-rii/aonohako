# CPP20 timing accuracy on a single Compute Engine CPU

The collection and analysis plans are frozen before any measurements on the guest. Six cases from integer recurrence and REP copy train the CPP20 models. Fifteen cases from six other programs evaluate their predictions. All eight executables use Aonohako's C++20 compile flags. The five reference sources come from `internal/durante/reference/fixtures` at the recorded Aonohako commit. The three additional sources are in `corpus/sources`.

In addition to comparing different held-out programs, the two inputs to `cycle_walk` directly test the effect of locality. Both use the same executable, allocate and initialize the same table, and perform 2,097,573 dependent loads. Only the active footprint changes, from 32 KiB to 64 MiB. Report the measured time and counter ratios for these two inputs and apply the same coefficient-independent error bound to this fixed pair.

[Collection schedule](protocol.json) · [Analysis plan](analysis-plan.json) · [Counterexample selection](witness-plan.json) · [Inputs and expected outputs](corpus/cases.json)
