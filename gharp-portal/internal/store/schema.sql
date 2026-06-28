CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY,
  github_id     INTEGER NOT NULL UNIQUE,
  github_login  TEXT    NOT NULL,
  role          TEXT    NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
  status        TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','invited')),
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS slots (
  id            TEXT    PRIMARY KEY,
  os_user       TEXT    NOT NULL,
  docker_host   TEXT    NOT NULL,
  network       TEXT    NOT NULL,
  base_url      TEXT    NOT NULL,
  internal_addr TEXT    NOT NULL,
  cpu_limit     TEXT    NOT NULL DEFAULT '',
  mem_limit     TEXT    NOT NULL DEFAULT '',
  max_runners   INTEGER NOT NULL DEFAULT 4,
  status        TEXT    NOT NULL DEFAULT 'free' CHECK (status IN ('free','assigned','disabled')),
  admin_token   TEXT    NOT NULL DEFAULT '',
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS assignments (
  user_id      INTEGER NOT NULL UNIQUE REFERENCES users(id),
  slot_id      TEXT    NOT NULL UNIQUE REFERENCES slots(id),
  gharp_state  TEXT    NOT NULL DEFAULT 'stopped' CHECK (gharp_state IN ('stopped','starting','running','error')),
  assigned_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
  token       TEXT PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id),
  csrf        TEXT NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  expires_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
  id          INTEGER PRIMARY KEY,
  actor_id    INTEGER REFERENCES users(id),
  action      TEXT NOT NULL,
  target      TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '',
  at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
