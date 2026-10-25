/* SPDX-License-Identifier: LGPL-2.0-or-later
 * Copyright (C) 2026 Crewship contributors
 *
 * Linux >= 3.17 preserves a mount's current atime flags on remount when
 * MS_NOATIME, MS_NODIRATIME, MS_RELATIME and MS_STRICTATIME are all absent.
 * bwrap is only adding security flags here, not changing atime policy. Avoid
 * re-specifying noatime bits that a closed seccomp policy need not authorize.
 * Leave normal relatime remounts unchanged for existing policy compatibility.
 */
static inline unsigned long
preserve_atime_flags (unsigned long flags)
{
  if (flags & (MS_NOATIME | MS_NODIRATIME))
    return flags & ~(MS_NOATIME | MS_NODIRATIME | MS_RELATIME);
  return flags;
}
