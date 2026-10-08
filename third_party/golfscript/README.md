# GolfScript interpreter

`golfscript.rb` is an unmodified copy of the MIT-licensed interpreter from
[darrenks/golfscript](https://github.com/darrenks/golfscript/blob/cded542533c2c8f72ab2d5935714f739b0357690/golfscript.rb),
pinned to commit `cded542533c2c8f72ab2d5935714f739b0357690`.

SHA-256: `84f932a624b19afe6ef2a3ebe09a9b832f765a87460a79cde22323615c468f38`.

aonohako invokes it with `-n`, which disables Ruby interpolation in GolfScript
strings, including strings evaluated by `~`. Standard GolfScript stack operations,
input as one byte string, and implicit output remain enabled. The interpreter
runs inside the existing Ruby execution sandbox.
