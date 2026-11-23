#!/usr/bin/env node
import { openSync, readSync, closeSync } from 'node:fs';
import { validateSubmission, runProject, LIMITS } from './entry-runtime.mjs';

// Read only the immutable submission and stdin. Expected state never enters
// this process. All diagnostics are fixed strings, not contestant/test values.
function readBounded(path, maximum) {
  const fd = path === 0 ? 0 : openSync(path, 'r');
  try {
    const buffer = Buffer.alloc(maximum + 1);
    let size = 0;
    while (size < buffer.length) {
      const count = readSync(fd, buffer, size, buffer.length - size, null);
      if (!count) break;
      size += count;
    }
    if (size > maximum) throw new Error('Input exceeds limits');
    return buffer.toString('utf8', 0, size);
  } finally { if (fd !== 0) closeSync(fd); }
}

try {
  const check = process.argv[2] === '--check';
  const path = process.argv[check ? 3 : 2];
  if (!path || process.argv.length !== 4) throw new Error('Invalid runner arguments');
  const submission = readBounded(path, LIMITS.submissionBytes);
  const plan = validateSubmission(submission);
  if (!check) {
    if (plan.mode !== process.argv[3]) throw new Error('Judging mode does not match submission');
    const result = runProject(submission, readBounded(0, LIMITS.inputBytes));
    process.stdout.write(plan.mode === 'state' ? JSON.stringify(result.state) + '\n' : result.stdout);
  }
} catch (error) {
  // Runtime errors only contain our fixed messages. JSON parser exceptions and
  // untrusted strings are never reflected into hidden-test diagnostics.
  const limited = error instanceof Error && ['Instruction limit exceeded', 'Logical tick limit exceeded'].includes(error.message);
  process.stderr.write(limited ? 'Entry execution limit exceeded\n' : 'Entry execution failed\n');
  process.exitCode = limited ? 124 : 1;
}
