-- Migration number: 0001 	 2026-11-15T00:00:00.000Z
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE, -- one account per address; a ';' in a comment
  created_at TEXT DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO users (email) VALUES ('first@example.com');
