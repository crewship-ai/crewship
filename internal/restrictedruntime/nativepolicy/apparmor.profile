abi <abi/4.0>,
profile crewship-native-codex-v159 flags=(attach_disconnected,mediate_deleted) {
  userns,
  pivot_root oldroot=/newroot/ /newroot/,
  pivot_root oldroot=/tmp/oldroot/ /tmp/,

  network,
  capability,
  file,
  umount,
  # Host (privileged) processes may send signals to container processes.
  signal (receive) peer=unconfined,
  # runc may send signals to container processes (for "docker stop").
  signal (receive) peer=runc,
  # crun may send signals to container processes (for "docker stop" when used with crun OCI runtime).
  signal (receive) peer=crun,
  # dockerd may send signals to container processes (for "docker kill").
  signal (receive) peer=unconfined,
  # Container processes may send signals amongst themselves.
  signal (send,receive) peer=crewship-native-codex-v159,

  deny /proc/* w,   # deny write for all files directly in /proc (not in a subdir)
  # deny write to files not in /proc/<number>/** or /proc/sys/**
  deny /proc/{[^1-9],[^1-9][^0-9],[^1-9s][^0-9y][^0-9s],[^1-9][^0-9][^0-9][^0-9/]*}/** w,
  deny /proc/sys/[^k]** w,  # deny /proc/sys except /proc/sys/k* (effectively /proc/sys/kernel)
  deny /proc/sys/kernel/{?,??,[^s][^h][^m]**} w,  # deny everything except shm* in /proc/sys/kernel/
  deny /proc/sysrq-trigger rwklx,
  deny /proc/kcore rwklx,

  mount options=(rw,rbind,silent) /tmp/newroot/ -> /tmp/newroot/,
  mount options=(rw,rbind,silent) /oldroot/dev/null -> /newroot/dev/null,
  remount options=(ro,nosuid,nodev,noexec,bind,silent,relatime) /newroot/proc/,
  mount options=(rw,rbind,silent) /oldroot/dev/zero -> /newroot/dev/zero,
  remount options=(ro,nosuid,nodev,bind,silent,relatime) /newroot/proc/**,
  remount options=(ro,nosuid,nodev,bind,silent) /newroot/proc/**,
  mount options=(rw,rbind,silent) /oldroot/dev/full -> /newroot/dev/full,
  remount options=(ro,nosuid,nodev,bind,silent,relatime) /newroot/**,
  remount options=(ro,nosuid,nodev,bind,silent) /newroot/**,
  remount options=(ro,nosuid,nodev,noexec,bind,silent,relatime) /newroot/**,
  mount options=(rw,rbind,silent) /oldroot/dev/random -> /newroot/dev/random,
  mount options=(rw,rbind,silent) /oldroot/dev/urandom -> /newroot/dev/urandom,
  mount options=(rw,rbind,silent) /oldroot/dev/tty -> /newroot/dev/tty,
  mount options=(rw,rbind,silent) /oldroot/tmp/ -> /newroot/tmp/,
  mount options=(rw,rbind,silent) /oldroot/tmp/codex-bwrap-synthetic-mount-targets-1001/ -> /newroot/tmp/codex-bwrap-synthetic-mount-targets-1001/,
  mount options=(rw,rbind,silent) /oldroot/home/agent/** -> /newroot/home/agent/**,
  mount options=(rw,rbind,silent) /oldroot/{usr,usr/bin,usr/sbin,usr/lib,usr/lib64,etc}/ -> /newroot/{usr,bin,sbin,lib,lib64,etc}/,
  mount options=(rw,silent,rprivate) -> /oldroot/,
  mount fstype=proc options=(rw,nosuid,nodev,noexec) -> /newroot/proc/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/home/agent/**/.*/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/tmp/.*/ ,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/tmp/.codex/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/tmp/.agents/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/tmp/.git/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/tmp/codex-daemon-1001/,
  mount fstype=devpts options=(rw,nosuid,noexec) -> /newroot/dev/pts/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/dev/,
  remount options=(ro,nosuid,nodev,bind,silent,relatime) /newroot/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /newroot/,
  mount options=(rw,rbind,silent) /oldroot/ -> /newroot/,
  mount fstype=tmpfs options=(rw,nosuid,nodev) -> /tmp/,
  mount options=(rw, silent, rslave) -> /,

  deny /sys/[^f]*/** wklx,
  deny /sys/f[^s]*/** wklx,
  deny /sys/fs/[^c]*/** wklx,
  deny /sys/fs/c[^g]*/** wklx,
  deny /sys/fs/cg[^r]*/** wklx,
  deny /sys/firmware/** rwklx,
  deny /sys/devices/virtual/powercap/** rwklx,
  deny /sys/kernel/security/** rwklx,

  # suppress ptrace denials when using 'docker ps' or using 'ps' inside a container
  ptrace (trace,read,tracedby,readby) peer=crewship-native-codex-v159,
}
