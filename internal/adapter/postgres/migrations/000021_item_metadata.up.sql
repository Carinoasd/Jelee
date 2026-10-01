BEGIN;
CREATE TABLE item_metadata_state (
 item_id uuid PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
 revision integer NOT NULL CHECK(revision BETWEEN 2 AND 2147483647)
);
CREATE TABLE item_metadata_fields (
 item_id uuid NOT NULL REFERENCES item_metadata_state(item_id) ON DELETE CASCADE,
 field text NOT NULL CHECK(field IN ('title','originalTitle','overview','date')),
 value text NOT NULL,
 source text NOT NULL CHECK(source IN ('existing','manual')),
 locked boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(item_id,field),
 CHECK(CASE field WHEN 'title' THEN CASE source WHEN 'existing' THEN length(value) BETWEEN 1 AND 1024 ELSE octet_length(value) BETWEEN 1 AND 1024 AND length(btrim(value))>0 END WHEN 'originalTitle' THEN octet_length(value)<=1024 WHEN 'overview' THEN octet_length(value)<=16384 WHEN 'date' THEN value='' OR (value ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' AND to_char(to_date(value,'YYYY-MM-DD'),'YYYY-MM-DD')=value AND left(value,4)<>'0000') ELSE false END)
);
COMMIT;
