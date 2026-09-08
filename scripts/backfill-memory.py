#!/usr/bin/env python3
"""
Compatibility passthrough for backfill-memory.

The original standalone backfill script was superseded by reconcile-memory.py,
which handles both initial backfill and incremental reconciliation idempotently.
A first reconcile run against an empty database is a backfill — every dedupe fetch
returns nothing, and every parsed row is therefore created.

This file exists so the old invocation name continues to work. It deliberately
holds no parsing or reconciliation logic of its own, so the two cannot drift
apart again (the defect that led to its removal).

The real implementation lives in reconcile-memory.py; this file execs it directly.
"""

import sys
import os

# Emit a compatibility notice to stderr so it's not captured by output parsers.
sys.stderr.write("Note: backfill-memory.py is retained for compatibility; reconcile-memory.py is the implementation.\n")
sys.stderr.flush()

# Locate reconcile-memory.py beside this script and exec it with all arguments.
script_dir = os.path.dirname(os.path.abspath(__file__))
reconciler = os.path.join(script_dir, "reconcile-memory.py")

# os.execv replaces this process, so the reconciler's exit code is passed through unchanged.
os.execv(sys.executable, [sys.executable, reconciler] + sys.argv[1:])
