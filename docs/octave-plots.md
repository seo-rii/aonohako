# Octave plot capture

Request `sidecar_outputs: [{"path":"__img__/images.jsonl"}]` to capture
Octave plots through the existing image event and sidecar contract. The control
plane must opt in for a problem whose image output may be shown to contestants;
do not enable this solely because the submission language is Octave. Images
can reveal input data from hidden tests.

The image-enabled command loads an image-owned helper, selects headless
gnuplot, and executes the original entry file in Octave's base workspace.
After successful script completion it snapshots up to 20 remaining open
figures, ordered by figure handle, as 1280 × 720 PNG images. Console output
and stdin keep their usual roles. Closed figures, intermediate animation
frames, explicit `exit` calls, script errors, and killed executions are not
captured. The helper does not inspect workspace variables.

Each JSONL record retains the existing `{mime, b64, ts}` shape. Images larger
than the 1 MiB event budget are skipped, and the total log is capped at 8 MiB.
Rendering is charged to the submission's time, memory, and workspace limits.
Capture warnings go to stderr; they do not replace the program's stdout.

Image-enabled Octave runs permit the child processes and named pipes needed by
gnuplot and Ghostscript. The `mknod` exceptions permit only FIFO nodes with a
zero device argument; device and socket node creation remains denied. Those
processes inherit the sandbox's identity, resource limits,
and syscall restrictions; the default network and Unix socket denials remain
unchanged. Ordinary
Octave requests retain the direct script command and the default subprocess
denial. No graphical desktop, display server, or browser runtime is involved.

The `ci-octave` runtime image smoke test checks text execution, stdin/stdout,
empty script arguments, two figures, and valid PNG bytes in the JSONL payload.
