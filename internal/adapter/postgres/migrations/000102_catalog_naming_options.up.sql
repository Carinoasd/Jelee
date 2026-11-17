BEGIN;
-- G21.3: per-library naming rules. Both default to off, the conservative
-- parser behaviour every library had before. The synchronisation stores the
-- parser version together with the enabled options on every derived row, so
-- changing an option plans the library's files again on the next run.
ALTER TABLE libraries ADD COLUMN naming_date_episodes boolean NOT NULL DEFAULT false;
ALTER TABLE libraries ADD COLUMN naming_absolute_episodes boolean NOT NULL DEFAULT false;

-- File-name facts of a synchronised item that the season/episode columns do
-- not carry: the last season of a cross-season file (episode_end then
-- numbers the episode in that season), the absolute number an absolute
-- library filed under season 1, the air date of a daily episode and the
-- special classification of Season 0 episodes and theatrical movies.
ALTER TABLE catalog_scan_items ADD COLUMN season_end integer CHECK(season_end BETWEEN 1 AND 100000);
ALTER TABLE catalog_scan_items ADD COLUMN absolute integer CHECK(absolute BETWEEN 1 AND 100000);
ALTER TABLE catalog_scan_items ADD COLUMN air_date date;
ALTER TABLE catalog_scan_items ADD COLUMN special text CHECK(special IN ('season','sp','ova','oad','extra','theatrical'));
ALTER TABLE catalog_scan_items ADD CONSTRAINT catalog_scan_items_season_range_check CHECK(season_end IS NULL OR season IS NOT NULL AND season_end>season);
COMMIT;
