/* SPDX-License-Identifier: LGPL-2.0-or-later */
#include <assert.h>
#include <sys/mount.h>
#include "../crewship-atime.h"

int main(void)
{
  const unsigned long atime_mask = MS_NOATIME | MS_NODIRATIME | MS_RELATIME;
  for (unsigned int security = 0; security < 16; security++)
    for (unsigned int atime = 0; atime < 8; atime++)
      {
        unsigned long input = (security & 1 ? MS_RDONLY : 0) |
          (security & 2 ? MS_NOSUID : 0) | (security & 4 ? MS_NODEV : 0) |
          (security & 8 ? MS_NOEXEC : 0) |
          (atime & 1 ? MS_NOATIME : 0) | (atime & 2 ? MS_NODIRATIME : 0) |
          (atime & 4 ? MS_RELATIME : 0);
        unsigned long output = preserve_atime_flags(input);
        /* Every security flag must survive unchanged. */
        assert((input & ~atime_mask) == (output & ~atime_mask));
        /* Model the documented kernel preservation, including locked flags. */
        unsigned long effective = output;
        if (!(output & atime_mask))
          effective |= input & atime_mask;
        assert(effective == input);
        if (!(input & (MS_NOATIME | MS_NODIRATIME)))
          assert(output == input);
      }
  return 0;
}
