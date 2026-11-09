"""Benign exit paths; only the enumerated conditions are executable."""
import sys

condition = sys.stdin.readline().strip()
if condition == "normal":
    print("done")
elif condition == "system_exit_zero":
    print("done")
    raise SystemExit(0)
elif condition == "system_exit_nonzero":
    print("done")
    raise SystemExit(7)
elif condition == "exception":
    raise RuntimeError("intentional fixture exception")
elif condition == "immediate_exit":
    import os
    os._exit(0)
elif condition == "wall_timeout":
    import time
    time.sleep(3)
    print("unexpected completion")
elif condition == "cpu_timeout":
    value = 1
    while True:
        value = (value * 1664525 + 1013904223) & 0xFFFFFFFF
elif condition == "thread":
    import threading
    thread = threading.Thread(target=lambda: None)
    thread.start()
    thread.join()
    print("done")
else:
    raise ValueError("unknown fixed boundary fixture")
