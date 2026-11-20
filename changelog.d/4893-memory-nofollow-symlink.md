- **Memory rooted reads refuse a final-component symlink that stays inside the root (#4893).** `OpenRootNoFollow` and the readers built on it now `Lstat` the name and compare the opened file with it, as the Windows build already did, so `.proposed/a.md -> sib.md` is refused instead of reading `sib.md`.

