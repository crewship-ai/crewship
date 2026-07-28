# Migrations

One file per migration. The registry is this directory — there is no central
list to edit, which is the point: two people adding a migration add two files
and cannot conflict.

Filename: `<YYYYMMDDHHMMSS>_<snake_case_name>.sql`

    20260728143000_add_widget_flag.sql

Generate the stamp with `date -u +%Y%m%d%H%M%S`. Versions must be strictly
ascending, which for timestamps means chronological, so append — never insert
a stamp older than one already committed.

`post_deploy/` holds migrations that run AFTER the server starts serving, in
batches, instead of blocking the boot. Read `post_deploy/README.md` before
putting anything there — it is not a free performance win, it is a contract
about what the running code must tolerate.

Migrations needing Go (schema discovery at apply time, SQLite table rebuilds)
still live in the `legacyMigrations` slice in `../migrate.go` (`migrations` is now
the merged registry: that slice plus every file here). Everything expressible
as plain SQL belongs here.

Nothing at or below **v169** may move here or change: those numbers are applied
in databases nobody controls. See `../migrate_version_scheme.go`.
