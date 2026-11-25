-- Migration number: 0002 	 2026-11-15T00:00:01.000Z
CREATE TABLE posts (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  body TEXT NOT NULL DEFAULT 'it''s; fine'
);
CREATE INDEX posts_user ON posts(user_id);
CREATE TRIGGER posts_touch AFTER INSERT ON posts BEGIN
  UPDATE users SET created_at = created_at WHERE id = NEW.user_id;
END;
