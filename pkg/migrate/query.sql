-- name: GetAppliedMigrations :many
select * from applied_migrations
  order by applied_at asc;

-- name: RecordMigration :exec
insert into applied_migrations
  (migration_filename, applied_at, ddl)
  values (?, ?, ?);

