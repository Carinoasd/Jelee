BEGIN;
-- Container layout of each media source (G10.6, G10.10): where an MP4/MOV
-- keeps its movie box and whether a Matroska file has a seek index. It is a
-- rebuildable cache read from bounded file headers during catalog sync,
-- valid only for the scan stamp (size and modification time) it was read
-- against; files are never modified. Rows go with their source.
CREATE TABLE media_source_layouts (
 source_id uuid PRIMARY KEY REFERENCES media_sources(id) ON DELETE CASCADE,
 size bigint NOT NULL CHECK(size>=0),
 modified_unix_nano bigint NOT NULL,
 reader_version text NOT NULL CHECK(octet_length(reader_version) BETWEEN 1 AND 32),
 layout text NOT NULL CHECK(layout IN ('moov_before_mdat','moov_at_end','fragmented','cues_before_clusters','cues_after_clusters','no_cues','unknown')),
 index_offset bigint NOT NULL DEFAULT 0 CHECK(index_offset>=0),
 index_length bigint NOT NULL DEFAULT 0 CHECK(index_length BETWEEN 0 AND 1073741824),
 checked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(index_length>0 OR index_offset=0),
 CHECK(index_offset<=size-index_length)
);
COMMIT;
