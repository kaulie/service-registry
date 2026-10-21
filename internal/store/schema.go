package store

// schema 是全部建表语句（幂等，启动时执行）。
const schema = `
CREATE TABLE IF NOT EXISTS namespaces (
  name        TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  token_hash  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS services (
  namespace     TEXT NOT NULL,
  name          TEXT NOT NULL,
  type          TEXT NOT NULL DEFAULT 'service',
  app_id        TEXT NOT NULL DEFAULT '',
  os            TEXT NOT NULL DEFAULT '',
  version       TEXT NOT NULL DEFAULT '',
  owner         TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',
  git_repo_url  TEXT NOT NULL DEFAULT '',
  department_id   TEXT NOT NULL DEFAULT '',
  department_name TEXT NOT NULL DEFAULT '',
  tags          TEXT NOT NULL DEFAULT '[]',
  protocols     TEXT NOT NULL DEFAULT '[]',
  base_path     TEXT NOT NULL DEFAULT '',
  health_path   TEXT NOT NULL DEFAULT '',
  mcp_path      TEXT NOT NULL DEFAULT '/mcp',
  auth_schemes  TEXT NOT NULL DEFAULT '[]',
  docs_url      TEXT NOT NULL DEFAULT '',
  spec_url      TEXT NOT NULL DEFAULT '',
  spec          TEXT NOT NULL DEFAULT '',
  spec_format   TEXT NOT NULL DEFAULT '',
  spec_hash     TEXT NOT NULL DEFAULT '',
  revision      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  registered_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (namespace, name)
);

CREATE TABLE IF NOT EXISTS endpoints (
  namespace    TEXT NOT NULL,
  service      TEXT NOT NULL,
  method       TEXT NOT NULL,
  path         TEXT NOT NULL,
  summary      TEXT NOT NULL DEFAULT '',
  operation_id TEXT NOT NULL DEFAULT '',
  tags         TEXT NOT NULL DEFAULT '[]',
  auth         TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (namespace, service, method, path)
);
CREATE INDEX IF NOT EXISTS idx_endpoints_path ON endpoints(path, method);

CREATE TABLE IF NOT EXISTS instances (
  id            TEXT PRIMARY KEY,
  namespace     TEXT NOT NULL,
  service       TEXT NOT NULL,
  scheme        TEXT NOT NULL,
  host          TEXT NOT NULL,
  port          INTEGER NOT NULL,
  os            TEXT NOT NULL DEFAULT '',
  arch          TEXT NOT NULL DEFAULT '',
  metadata      TEXT NOT NULL DEFAULT '{}',
  revision      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  registered_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_instances_service ON instances(namespace, service);
CREATE UNIQUE INDEX IF NOT EXISTS idx_instances_addr
  ON instances(namespace, service, scheme, host, port);

CREATE TABLE IF NOT EXISTS changes (
  revision  INTEGER PRIMARY KEY AUTOINCREMENT,
  at        TEXT NOT NULL,
  namespace TEXT NOT NULL DEFAULT '',
  entity    TEXT NOT NULL,
  ref       TEXT NOT NULL,
  op        TEXT NOT NULL,
  actor     TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_changes_namespace ON changes(namespace, revision);
CREATE INDEX IF NOT EXISTS idx_changes_ref ON changes(ref, revision);

CREATE TABLE IF NOT EXISTS agent_mcp_servers (
  name          TEXT PRIMARY KEY,
  namespace     TEXT NOT NULL,
  transport     TEXT NOT NULL,
  command       TEXT NOT NULL DEFAULT '',
  args          TEXT NOT NULL DEFAULT '[]',
  env           TEXT NOT NULL DEFAULT '{}',
  cwd           TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  headers       TEXT NOT NULL DEFAULT '{}',
  revision      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  registered_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_agent_mcp_servers_namespace ON agent_mcp_servers(namespace);
`

// postgresSchema 与 schema 同一套表，只把 SQLite 专有的 AUTOINCREMENT 换成 IDENTITY。
// 已有库靠 CREATE TABLE IF NOT EXISTS 跳过，缺列仍走 columnMigrations。
const postgresSchema = `
CREATE TABLE IF NOT EXISTS namespaces (
  name        TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  token_hash  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS services (
  namespace     TEXT NOT NULL,
  name          TEXT NOT NULL,
  type          TEXT NOT NULL DEFAULT 'service',
  app_id        TEXT NOT NULL DEFAULT '',
  os            TEXT NOT NULL DEFAULT '',
  version       TEXT NOT NULL DEFAULT '',
  owner         TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',
  git_repo_url  TEXT NOT NULL DEFAULT '',
  department_id   TEXT NOT NULL DEFAULT '',
  department_name TEXT NOT NULL DEFAULT '',
  tags          TEXT NOT NULL DEFAULT '[]',
  protocols     TEXT NOT NULL DEFAULT '[]',
  base_path     TEXT NOT NULL DEFAULT '',
  health_path   TEXT NOT NULL DEFAULT '',
  mcp_path      TEXT NOT NULL DEFAULT '/mcp',
  auth_schemes  TEXT NOT NULL DEFAULT '[]',
  docs_url      TEXT NOT NULL DEFAULT '',
  spec_url      TEXT NOT NULL DEFAULT '',
  spec          TEXT NOT NULL DEFAULT '',
  spec_format   TEXT NOT NULL DEFAULT '',
  spec_hash     TEXT NOT NULL DEFAULT '',
  revision      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  registered_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (namespace, name)
);

CREATE TABLE IF NOT EXISTS endpoints (
  namespace    TEXT NOT NULL,
  service      TEXT NOT NULL,
  method       TEXT NOT NULL,
  path         TEXT NOT NULL,
  summary      TEXT NOT NULL DEFAULT '',
  operation_id TEXT NOT NULL DEFAULT '',
  tags         TEXT NOT NULL DEFAULT '[]',
  auth         TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (namespace, service, method, path)
);
CREATE INDEX IF NOT EXISTS idx_endpoints_path ON endpoints(path, method);

CREATE TABLE IF NOT EXISTS instances (
  id            TEXT PRIMARY KEY,
  namespace     TEXT NOT NULL,
  service       TEXT NOT NULL,
  scheme        TEXT NOT NULL,
  host          TEXT NOT NULL,
  port          INTEGER NOT NULL,
  os            TEXT NOT NULL DEFAULT '',
  arch          TEXT NOT NULL DEFAULT '',
  metadata      TEXT NOT NULL DEFAULT '{}',
  revision      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  registered_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_instances_service ON instances(namespace, service);
CREATE UNIQUE INDEX IF NOT EXISTS idx_instances_addr
  ON instances(namespace, service, scheme, host, port);

CREATE TABLE IF NOT EXISTS changes (
  revision  INTEGER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  at        TEXT NOT NULL,
  namespace TEXT NOT NULL DEFAULT '',
  entity    TEXT NOT NULL,
  ref       TEXT NOT NULL,
  op        TEXT NOT NULL,
  actor     TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_changes_namespace ON changes(namespace, revision);
CREATE INDEX IF NOT EXISTS idx_changes_ref ON changes(ref, revision);

CREATE TABLE IF NOT EXISTS agent_mcp_servers (
  name          TEXT PRIMARY KEY,
  namespace     TEXT NOT NULL,
  transport     TEXT NOT NULL,
  command       TEXT NOT NULL DEFAULT '',
  args          TEXT NOT NULL DEFAULT '[]',
  env           TEXT NOT NULL DEFAULT '{}',
  cwd           TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  headers       TEXT NOT NULL DEFAULT '{}',
  revision      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  registered_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_agent_mcp_servers_namespace ON agent_mcp_servers(namespace);
`
