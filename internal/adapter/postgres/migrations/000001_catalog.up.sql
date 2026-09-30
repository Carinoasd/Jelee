CREATE TABLE users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 128),
 is_admin boolean NOT NULL DEFAULT false,
 disabled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
 client_kind text NOT NULL CHECK (client_kind IN ('web','native')),
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz
);
CREATE INDEX sessions_user_idx ON sessions(user_id);
CREATE TABLE libraries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 128)
);
CREATE TABLE library_roots (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 path text NOT NULL UNIQUE CHECK (length(path)>0),
 UNIQUE(id,library_id)
);
CREATE TABLE library_acl (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 PRIMARY KEY(user_id,library_id)
);
CREATE TABLE items (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 title text NOT NULL CHECK (length(title) BETWEEN 1 AND 1024),
 kind text NOT NULL CHECK (kind IN ('Movie','Series','Season','Episode','HomeVideo')),
 UNIQUE(id,library_id)
);
CREATE INDEX items_library_id_idx ON items(library_id,id);
CREATE TABLE media_sources (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 item_id uuid NOT NULL,
 library_id uuid NOT NULL,
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK (length(relative_path)>0),
 content_type text NOT NULL CHECK (content_type IN ('video/mp4','video/x-matroska','video/webm','video/quicktime','video/x-msvideo','video/mp2t')),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE CASCADE,
 UNIQUE(root_id,relative_path)
);
CREATE INDEX media_sources_item_idx ON media_sources(item_id);
CREATE TABLE audit_logs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 event text NOT NULL,
 target_id uuid NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT now()
);
