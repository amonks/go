create table applied_migrations (
  migration_filename text not null primary key,
  applied_at text not null,
  ddl text not null,

  unique index applied_at
);

